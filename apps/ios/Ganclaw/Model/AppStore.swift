import Foundation
import Observation

/// Owns AppState and runs side effects. Views call `send` for pure state
/// changes and the intent methods below for anything that does I/O.
@MainActor
@Observable
final class AppStore {
    private(set) var state = AppState()

    @ObservationIgnored private var client: APIClient?
    @ObservationIgnored private let events = EventStream()
    /// Every device access goes through here (Platform/).
    @ObservationIgnored let platform: Platform
    @ObservationIgnored private var attachmentData: [String: Data] = [:]
    @ObservationIgnored private var toastTask: Task<Void, Never>?
    /// Every action applied, newest last; the control server exposes it.
    @ObservationIgnored private(set) var actionLog: [String] = []
    /// Background results waiting to be read out in drive mode.
    @ObservationIgnored private var announcements: [Announcement] = []
    private struct Announcement { var thread: String; var id: Int64; var text: String }
    @ObservationIgnored private var announced: Set<Int64> = []

    init(platform: Platform) {
        self.platform = platform
        send(.speechPreferences(platform.speech.loadPreferences()))
        events.onEvent = { [weak self] e in self?.handle(event: e) }
        events.onStatus = { [weak self] s in self?.handle(status: s) }
        if let credentials = platform.keychain.load() { connect(credentials) }
        platform.notifications.observe({ [weak self] token in
            guard let self, let client = self.client else { return }
            Task {
                do { try await client.registerPush(token) }
                catch { self.send(.banner("Could not enable notifications. Try again in Settings.")) }
            }
        }, { [weak self] thread in
            guard let self, self.client != nil else { return }
            Task {
                await self.refresh()
                guard self.state.agents.contains(where: { $0.name == thread }) else { return }
                self.openThread(thread)
            }
        }, { [weak self] message in self?.send(.banner(message)) })
    }

    func send(_ action: AppState.Action) {
        let oldUnread = state.totalUnread
        state.apply(action)
        if state.totalUnread != oldUnread {
            let count = state.totalUnread
            Task { await platform.notifications.setBadge(count) }
        }
        actionLog.append(String(describing: action).prefix(200).description)
        if actionLog.count > 500 { actionLog.removeFirst(actionLog.count - 500) }
    }

    var api: APIClient? { client }

    func setSpeechPreferences(_ preferences: SpeechPreferences) {
        send(.speechPreferences(preferences))
        platform.speech.savePreferences(preferences)
    }

    func previewVoice() {
        platform.speech.speak("Hello. This is how your agent replies will sound.") {}
    }

    /// Selects a voice ("" for the system default) and plays it.
    func chooseVoice(_ id: String) {
        var value = state.speechPreferences
        value.voiceID = id
        setSpeechPreferences(value)
        previewVoice()
    }

    // MARK: Pairing

    func pair() async {
        let p = state.pairing
        var serverText = p.server.trimmingCharacters(in: .whitespacesAndNewlines)
        if !serverText.contains("://") { serverText = "https://" + serverText }
        guard let server = URL(string: serverText), server.host != nil else {
            send(.pairFailed("Enter the server address, e.g. https://ganclaw.example.com"))
            return
        }
        let code = p.code.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !code.isEmpty else {
            send(.pairFailed("Enter the code from `ganclaw app pair`."))
            return
        }
        send(.pairStarted)
        let name = platform.device.name()
        do {
            let r = try await APIClient(baseURL: server).pair(code: code, deviceName: name)
            let creds = Credentials(server: server, token: r.token, deviceName: r.device.name)
            platform.keychain.save(creds)
            connect(creds)
        } catch {
            send(.pairFailed(describe(error)))
        }
    }

    func open(link: PairLink) async {
        send(.pairFields(server: link.server.absoluteString, code: link.code))
        if state.connection == .unpaired { await pair() }
    }

    func connect(_ c: Credentials) {
        client = APIClient(baseURL: c.server, token: c.token)
        send(.paired(server: c.server, deviceName: c.deviceName))
        events.connect(client!)
        Task { await syncNotifications(); await refresh() }
    }

    func unpair(reason: String? = nil) {
        if reason == nil, let client {
            // Tell the server first so it stops notifying this phone, but
            // never trap the user in a pairing with a server that's gone.
            Task {
                do {
                    try await client.unpair()
                    self.unpair(reason: "")
                } catch {
                    self.unpair(reason: "Unpaired this phone, but the server couldn't be reached. It may keep sending notifications until you run `ganclaw app unpair` there.")
                }
            }
            return
        }
        events.disconnect()
        platform.speech.stopListening()
        platform.speech.stopSpeaking()
        client = nil
        platform.keychain.clear()
        attachmentData = [:]
        send(.unpaired(reason: reason == "" ? nil : reason))
    }

    func enableNotifications() async {
        guard client != nil else { return }
        do { try await platform.notifications.enable() }
        catch { send(.banner(describe(error))) }
    }

    func syncNotifications() async {
        guard let client else { return }
        if !(await platform.notifications.registerIfAuthorized()) {
            try? await client.removePush()
        }
    }

    // MARK: Loading

    func refresh() async {
        guard let client else { return }
        do {
            async let agents = client.agents()
            async let jobs = client.jobs()
            let (a, j) = try await (agents, jobs)
            send(.agentsLoaded(a))
            await platform.notifications.setBadge(state.totalUnread)
            send(.jobsLoaded(j))
            send(.banner(nil))
            for case .thread(let name) in state.path { await loadThread(name) }
        } catch {
            fail(error)
        }
    }

    func loadThread(_ agent: String) async {
        guard let client else { return }
        send(.threadLoading(agent))
        do {
            let page = try await client.messages(agent: agent)
            send(.threadLoaded(agent, page.messages, page.state))
        } catch {
            send(.threadFailed(agent, describe(error)))
            fail(error, quiet: true)
        }
    }

    func loadJobRuns(_ job: String) async {
        guard let client else { return }
        do {
            send(.jobRunsLoaded(job, try await client.jobRuns(job: job)))
        } catch {
            fail(error)
        }
    }

    /// Marks a thread read through `id`. Views call it when the user is
    /// looking at the conversation (bottom visible, typing, scrolling); drive
    /// mode calls it with `heard: true` for a reply it read aloud, since the
    /// thread isn't on screen then.
    func markRead(_ agent: String, through id: Int64, heard: Bool = false) async {
        guard let client, id > (state.agent(agent)?.lastReadId ?? 0),
              heard || state.currentScreen == "thread:\(agent)" else { return }
        do { send(.readState(agent, try await client.markRead(agent: agent, messageID: id))) }
        catch { fail(error, quiet: true) }
    }

    // MARK: Navigation

    func openThread(_ agent: String) {
        if state.path.last != .thread(agent) { send(.push(.thread(agent))) }
        Task { await loadThread(agent) }
    }

    func openJob(_ job: String) {
        send(.push(.job(job)))
        Task { await loadJobRuns(job) }
    }

    /// "Ask about this run": open the job's agent thread replying to it.
    func ask(about run: AppMessage) {
        send(.setPath([.thread(run.thread)]))
        send(.replyTo(run.thread, run.id))
        Task { await loadThread(run.thread) }
    }

    // MARK: Messages

    func sendDraft(_ agent: String, voice: Bool = false) async {
        let t = state.thread(agent)
        let text = t.draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, !t.sending else { return }
        await sendMessage(agent, text: text, fileIDs: [], replyTo: t.replyTo, voice: voice)
    }

    @discardableResult
    func sendMessage(_ agent: String, text: String, fileIDs: [String], replyTo: Int64?, voice: Bool, driveMode: Bool = false) async -> AppMessage? {
        guard let client else { return nil }
        send(.sending(agent, true))
        do {
            let m = try await client.send(agent: agent, text: text, fileIDs: fileIDs, replyTo: replyTo, voice: voice, driveMode: driveMode)
            send(.sent(m))
            return m
        } catch {
            send(.sending(agent, false))
            fail(error)
            return nil
        }
    }

    /// Taps a reply button: answers that message with the label.
    func choose(_ label: String, on message: AppMessage) async {
        await sendMessage(message.thread, text: label, fileIDs: [], replyTo: message.id, voice: false)
    }

    func stop(_ agent: String) async {
        do { try await client?.stop(agent: agent) } catch { fail(error) }
    }

    func reset(_ agent: String) async {
        do { try await client?.reset(agent: agent) } catch { fail(error) }
    }

    // MARK: Attachments

    func addAttachment(name: String, mime: String, data: Data) {
        guard let agent = state.attach.agent, let client else { return }
        let id = UUID().uuidString
        attachmentData[id] = data
        send(.attachAdded(PendingAttachment(id: id, name: name, mime: mime, size: Int64(data.count))))
        Task {
            do {
                let f = try await client.upload(agent: agent, name: name, mime: mime, data: data)
                send(.attachUploaded(id, f))
            } catch {
                send(.attachFailed(id, describe(error)))
            }
            attachmentData[id] = nil
        }
    }

    func removeAttachment(_ id: String) {
        attachmentData[id] = nil
        send(.attachRemoved(id))
    }

    func sendAttachments() async {
        let a = state.attach
        guard let agent = a.agent, a.ready, !a.sending else { return }
        send(.attachSending(true))
        let ids = a.pending.compactMap { $0.file?.id }
        let note = a.note.trimmingCharacters(in: .whitespacesAndNewlines)
        let ok = await sendMessage(agent, text: note, fileIDs: ids, replyTo: state.thread(agent).replyTo, voice: false) != nil
        send(.attachSending(false))
        if ok { send(.attachClose) }
    }

    // MARK: Copy blocks

    func copy(_ text: String, block: String, label: String) {
        platform.pasteboard.setString(text)
        send(.copied(block))
        showToast("\(label) copied")
        Task {
            try? await Task.sleep(nanoseconds: 1_600_000_000)
            if state.copiedBlock == block { send(.copied(nil)) }
        }
    }

    func showToast(_ text: String) {
        toastTask?.cancel()
        send(.toast(text))
        toastTask = Task {
            try? await Task.sleep(nanoseconds: 1_800_000_000)
            if !Task.isCancelled { send(.toast(nil)) }
        }
    }

    // MARK: Drive mode

    func startDrive(_ agent: String?) async {
        guard let agent = agent ?? state.sortedAgents.first?.name else { return }
        send(.driveStart(agent))
        do {
            try await platform.speech.requestAccess()
            try listen()
            platform.device.setIdleTimerDisabled(true)
        } catch {
            send(.driveError(describe(error)))
        }
        Task { await loadThread(agent) }
    }

    func exitDrive() {
        announcements = []
        platform.speech.stopListening()
        platform.speech.stopSpeaking()
        platform.device.setIdleTimerDisabled(false)
        send(.driveExit)
    }

    func setDriveMuted(_ muted: Bool) {
        send(.driveMuted(muted))
        if muted {
            platform.speech.stopListening()
        } else {
            do { try listen() } catch { send(.driveError(describe(error))) }
        }
    }

    func repeatLastAnswer() {
        guard let a = state.drive.lastAnswer else { return }
        send(.drivePhase(.speaking))
        platform.speech.speak(a) { [weak self] in self?.spokeReply() }
    }

    private func listen() throws {
        try platform.speech.listen { [weak self] text, final in self?.heard(text, final: final) }
    }

    private func heard(_ text: String, final: Bool) {
        let d = state.drive
        guard d.active, !d.muted, let agent = d.agent else { return }
        if !final {
            send(.driveHeard(text))
            if d.phase == .speaking || d.phase == .idle { send(.drivePhase(.listening)); send(.driveHeard(text)) }
            return
        }
        let after = state.thread(agent).messages.last?.id ?? state.agent(agent)?.lastMessage?.id ?? 0
        send(.driveAsked(text, after: after))
        Task {
            if await sendMessage(agent, text: text, fileIDs: [], replyTo: nil, voice: d.shortAnswers, driveMode: true) == nil {
                send(.drivePhase(.listening))
            }
        }
    }

    private func spokeReply() {
        if state.drive.active, state.drive.phase == .speaking { send(.drivePhase(.listening)) }
        speakNextAnnouncement()
    }

    /// Speaks a finished reply in the drive thread. Job runs (scheduled
    /// reports, background work) aren't answers to the question.
    private func speakIfDriveReply(_ m: AppMessage) {
        let d = state.drive
        guard d.active, m.thread == d.agent, m.role != .user, m.status != .streaming, (m.job ?? "").isEmpty,
              m.id > d.askedAfter, d.lastQuestion != nil, d.lastAnswer == nil else { return }
        let (spoken, details) = m.spokenPart
        let answer = spoken.isEmpty ? "Done." : spoken
        send(.driveAnswered(answer, details: details || !m.files.isEmpty || !m.actions.isEmpty))
        platform.speech.speak(answer) { [weak self] in self?.spokeReply() }
        Task { await markRead(m.thread, through: m.id, heard: true) }
    }

    /// Reads out background work finishing in the drive thread, once the
    /// conversation is free (not while thinking about or speaking a reply).
    private func announceIfBackgroundResult(_ m: AppMessage) {
        let d = state.drive
        guard d.active, m.thread == d.agent, m.role != .user, m.status != .streaming,
              m.job == AppMessage.backgroundJob, announced.insert(m.id).inserted else { return }
        let spoken = m.spokenPart.spoken
        announcements.append(Announcement(thread: m.thread, id: m.id, text: spoken.isEmpty || spoken.count > 400
            ? "A background task finished. The details are in the thread." : spoken))
        speakNextAnnouncement()
    }

    private func speakNextAnnouncement() {
        guard state.drive.active, state.drive.phase == .listening, !announcements.isEmpty else { return }
        let next = announcements.removeFirst()
        send(.drivePhase(.speaking))
        platform.speech.speak(next.text) { [weak self] in self?.spokeReply() }
        Task { await markRead(next.thread, through: next.id, heard: true) }
    }

    // MARK: Events

    private func handle(event e: ServerEvent) {
        if e.type == "hello" {
            // Catch up on anything missed while disconnected.
            Task { await refresh() }
            return
        }
        send(.event(e))
        if e.type == "message", let m = e.message {
            speakIfDriveReply(m)
            announceIfBackgroundResult(m)
        }
    }

    private func handle(status s: EventStream.Status) {
        switch s {
        case .connected: send(.connection(.connected))
        case .connecting: if state.connection != .connected { send(.connection(.connecting)) }
        case .disconnected: send(.connection(.disconnected))
        }
    }

    private func fail(_ error: Error, quiet: Bool = false) {
        if let e = error as? APIError, e.isUnauthorized {
            unpair(reason: "This phone was unpaired. Run `ganclaw app pair` for a new code.")
            return
        }
        if !quiet { send(.banner(describe(error))) }
    }

    func describe(_ error: Error) -> String {
        if let e = error as? APIError { return e.message }
        if let e = error as? URLError {
            switch e.code {
            case .notConnectedToInternet: return "You're offline."
            case .cannotConnectToHost, .cannotFindHost, .timedOut: return "Can't reach the ganclaw server."
            case .serverCertificateUntrusted, .secureConnectionFailed: return "The server's certificate isn't trusted."
            default: break
            }
        }
        return error.localizedDescription
    }
}
