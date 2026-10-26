import Foundation
import Observation
import UIKit

/// Owns AppState and runs side effects. Views call `send` for pure state
/// changes and the intent methods below for anything that does I/O.
@MainActor
@Observable
final class AppStore {
    private(set) var state = AppState()

    @ObservationIgnored private var client: APIClient?
    @ObservationIgnored private let events = EventStream()
    @ObservationIgnored let speech: SpeechEngine
    @ObservationIgnored private var attachmentData: [String: Data] = [:]
    @ObservationIgnored private var toastTask: Task<Void, Never>?
    @ObservationIgnored private var persist: Bool
    /// Every action applied, newest last; the control server exposes it.
    @ObservationIgnored private(set) var actionLog: [String] = []

    init(credentials: Credentials? = nil, speech: SpeechEngine, persist: Bool = true) {
        self.speech = speech
        self.persist = persist
        events.onEvent = { [weak self] e in self?.handle(event: e) }
        events.onStatus = { [weak self] s in self?.handle(status: s) }
        speech.onHeard = { [weak self] text, final in self?.heard(text, final: final) }
        speech.onSpeakingDone = { [weak self] in self?.spokeReply() }
        if let credentials { connect(credentials) }
    }

    func send(_ action: AppState.Action) {
        state.apply(action)
        actionLog.append(String(describing: action).prefix(200).description)
        if actionLog.count > 500 { actionLog.removeFirst(actionLog.count - 500) }
    }

    var api: APIClient? { client }

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
        let name = UIDevice.current.name
        do {
            let r = try await APIClient(baseURL: server).pair(code: code, deviceName: name)
            let creds = Credentials(server: server, token: r.token, deviceName: r.device.name)
            if persist { CredentialStore.save(creds) }
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
        Task { await refresh() }
    }

    func unpair(reason: String? = nil) {
        events.disconnect()
        speech.stopListening()
        speech.stopSpeaking()
        client = nil
        if persist { CredentialStore.clear() }
        attachmentData = [:]
        send(.unpaired(reason: reason))
    }

    // MARK: Loading

    func refresh() async {
        guard let client else { return }
        do {
            async let agents = client.agents()
            async let jobs = client.jobs()
            let (a, j) = try await (agents, jobs)
            send(.agentsLoaded(a))
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
    func sendMessage(_ agent: String, text: String, fileIDs: [String], replyTo: Int64?, voice: Bool) async -> AppMessage? {
        guard let client else { return nil }
        send(.sending(agent, true))
        do {
            let m = try await client.send(agent: agent, text: text, fileIDs: fileIDs, replyTo: replyTo, voice: voice)
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
        UIPasteboard.general.string = text
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
        guard await speech.requestAccess() else {
            send(.driveError("Allow microphone and speech recognition in Settings to use drive mode."))
            return
        }
        do {
            try speech.startListening()
            UIApplication.shared.isIdleTimerDisabled = true
        } catch {
            send(.driveError(describe(error)))
        }
        Task { await loadThread(agent) }
    }

    func exitDrive() {
        speech.stopListening()
        speech.stopSpeaking()
        UIApplication.shared.isIdleTimerDisabled = false
        send(.driveExit)
    }

    func setDriveMuted(_ muted: Bool) {
        send(.driveMuted(muted))
        if muted { speech.stopListening() } else { try? speech.startListening() }
    }

    func repeatLastAnswer() {
        guard let a = state.drive.lastAnswer else { return }
        send(.drivePhase(.speaking))
        speech.speak(a)
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
            if await sendMessage(agent, text: text, fileIDs: [], replyTo: nil, voice: d.shortAnswers) == nil {
                send(.drivePhase(.listening))
            }
        }
    }

    private func spokeReply() {
        if state.drive.active, state.drive.phase == .speaking { send(.drivePhase(.listening)) }
    }

    /// Speaks a finished reply in the drive thread.
    private func speakIfDriveReply(_ m: AppMessage) {
        let d = state.drive
        guard d.active, m.thread == d.agent, m.role != .user, m.status != .streaming, m.id > d.askedAfter, d.lastQuestion != nil, d.lastAnswer == nil else { return }
        let (spoken, details) = m.spokenPart
        let answer = spoken.isEmpty ? "Done." : spoken
        send(.driveAnswered(answer, details: details || !m.files.isEmpty || !m.actions.isEmpty))
        speech.speak(answer)
    }

    // MARK: Events

    private func handle(event e: ServerEvent) {
        if e.type == "hello" {
            // Catch up on anything missed while disconnected.
            Task { await refresh() }
            return
        }
        send(.event(e))
        if e.type == "message", let m = e.message { speakIfDriveReply(m) }
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
