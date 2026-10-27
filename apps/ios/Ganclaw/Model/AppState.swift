import Foundation

// The app is a projection of AppState. Every change is an Action applied by
// `AppState.apply`, a pure function: no I/O, no clocks except what actions
// carry. Views render from state and dispatch actions; AppStore runs the
// side effects (network, speech) and feeds their results back as actions.
// The control server reads the same state, so tests see what the UI sees.

enum Route: Codable, Hashable {
    case thread(String)
    case settings
    case voiceSettings
    case jobs
    case job(String)
}

enum InboxFilter: String, Codable, CaseIterable, Identifiable {
    case all, running, needsYou, jobs
    var id: String { rawValue }
    var title: String {
        switch self {
        case .all: "All"
        case .running: "Running"
        case .needsYou: "Needs you"
        case .jobs: "Jobs"
        }
    }
}

enum ConnectionStatus: String, Codable {
    case unpaired, connecting, connected, disconnected
}

struct PairingState: Codable, Equatable {
    var server = ""
    var code = ""
    var busy = false
    var error: String?
}

struct ThreadState: Codable, Equatable {
    var messages: [AppMessage] = []
    var loaded = false
    var loading = false
    var error: String?
    var draft = ""
    /// The message the next send answers (set by "Ask about this run").
    var replyTo: Int64?
    /// Text streamed so far for replies still being written, by message ID.
    var streaming: [Int64: String] = [:]
    var sending = false

    func message(_ id: Int64) -> AppMessage? { messages.first { $0.id == id } }
}

struct PendingAttachment: Codable, Equatable, Identifiable {
    var id: String
    var name: String
    var mime: String
    var size: Int64
    var file: AppFile?
    var error: String?
    var uploading: Bool { file == nil && error == nil }
}

struct AttachState: Codable, Equatable {
    var presented = false
    var agent: String?
    var pending: [PendingAttachment] = []
    var note = ""
    var sending = false

    var ready: Bool { !pending.isEmpty && pending.allSatisfy { $0.file != nil } }
}

enum DrivePhase: String, Codable {
    case idle, listening, thinking, speaking
}

struct DriveState: Codable, Equatable {
    var active = false
    var agent: String?
    var muted = false
    var shortAnswers = true
    var phase: DrivePhase = .idle
    /// Live transcript of what the user is saying.
    var heard = ""
    var lastQuestion: String?
    var lastAnswer: String?
    /// True when the last reply had details beyond the spoken part.
    var transcriptPresented = false
    var detailsSaved = false
    /// Replies newer than this message ID are spoken.
    var askedAfter: Int64 = 0
    var error: String?
}

struct AppState: Codable, Equatable {
    var connection: ConnectionStatus = .unpaired
    var server: URL?
    var deviceName: String?
    var pairing = PairingState()

    var agents: [AgentSummary] = []
    var agentsLoaded = false
    var filter: InboxFilter = .all
    var threads: [String: ThreadState] = [:]
    var jobs: [JobSummary] = []
    var jobRuns: [String: [AppMessage]] = [:]

    var path: [Route] = []
    var attach = AttachState()
    var drive = DriveState()
    var speechPreferences = SpeechPreferences()

    var toast: String?
    /// Identifies the copy block whose button shows "Copied".
    var copiedBlock: String?
    var banner: String?

    // MARK: Derived

    var currentScreen: String {
        if connection == .unpaired { return "pair" }
        if drive.active { return "drive" }
        if attach.presented { return "attach" }
        switch path.last {
        case .thread(let a): return "thread:\(a)"
        case .settings: return "settings"
        case .voiceSettings: return "settings:voice"
        case .jobs: return "jobs"
        case .job(let j): return "job:\(j)"
        case nil: return "inbox"
        }
    }

    func thread(_ agent: String) -> ThreadState { threads[agent] ?? ThreadState() }

    func agent(_ name: String) -> AgentSummary? { agents.first { $0.name == name } }

    /// Inbox rows in display order: needs-you first, then working agents,
    /// then by latest activity, with agents that have never talked last.
    var sortedAgents: [AgentSummary] {
        agents.sorted { a, b in
            func rank(_ s: AgentSummary) -> Int { s.needsYou ? 0 : s.state.working ? 1 : s.lastMessage == nil ? 3 : 2 }
            let (ra, rb) = (rank(a), rank(b))
            if ra != rb { return ra < rb }
            let (ta, tb) = (a.lastMessage?.id ?? 0, b.lastMessage?.id ?? 0)
            if ta != tb { return ta > tb }
            return a.name < b.name
        }
    }

    var inboxRows: [AgentSummary] {
        switch filter {
        case .all, .jobs: sortedAgents
        case .running: sortedAgents.filter { $0.state.working }
        case .needsYou: sortedAgents.filter { $0.needsYou }
        }
    }

    var jobProblems: Int { jobs.reduce(0) { $0 + ($1.problems > 0 ? 1 : 0) } }

    // MARK: Actions

    enum Action: Equatable {
        case speechPreferences(SpeechPreferences)
        case pairFields(server: String, code: String)
        case pairStarted
        case pairFailed(String)
        case paired(server: URL, deviceName: String)
        case unpaired(reason: String?)
        case connection(ConnectionStatus)

        case agentsLoaded([AgentSummary])
        case threadLoading(String)
        case threadLoaded(String, [AppMessage], AgentState)
        case threadFailed(String, String)
        case jobsLoaded([JobSummary])
        case jobRunsLoaded(String, [AppMessage])
        case event(ServerEvent)

        case setFilter(InboxFilter)
        case setPath([Route])
        case push(Route)
        case pop

        case draft(String, String)
        case replyTo(String, Int64?)
        case sending(String, Bool)
        case sent(AppMessage)

        case attachOpen(String)
        case attachClose
        case attachAdded(PendingAttachment)
        case attachRemoved(String)
        case attachUploaded(String, AppFile)
        case attachFailed(String, String)
        case attachNote(String)
        case attachSending(Bool)

        case driveStart(String)
        case driveExit
        case driveTranscript(Bool)
        case driveAgent(String)
        case driveMuted(Bool)
        case driveShortAnswers(Bool)
        case drivePhase(DrivePhase)
        case driveHeard(String)
        case driveAsked(String, after: Int64)
        case driveAnswered(String, details: Bool)
        case driveError(String?)

        case toast(String?)
        case copied(String?)
        case banner(String?)
    }

    mutating func apply(_ action: Action) {
        switch action {
        case .speechPreferences(let preferences): speechPreferences = preferences
        case let .pairFields(server, code):
            pairing.server = server
            pairing.code = code
            pairing.error = nil
        case .pairStarted:
            pairing.busy = true
            pairing.error = nil
        case .pairFailed(let msg):
            pairing.busy = false
            pairing.error = msg
        case let .paired(server, name):
            self.server = server
            deviceName = name
            pairing = PairingState()
            connection = .connecting
        case .unpaired(let reason):
            self = AppState()
            pairing.error = reason
        case .connection(let s):
            if connection != .unpaired { connection = s }

        case .agentsLoaded(let list):
            agents = list
            agentsLoaded = true
        case .threadLoading(let a):
            threads[a, default: ThreadState()].loading = true
        case let .threadLoaded(a, msgs, st):
            var t = thread(a)
            t.messages = msgs
            t.loaded = true
            t.loading = false
            t.error = nil
            threads[a] = t
            setState(a, st)
            if let last = msgs.last { noteLast(last) }
        case let .threadFailed(a, msg):
            threads[a, default: ThreadState()].loading = false
            threads[a, default: ThreadState()].error = msg
        case .jobsLoaded(let j):
            jobs = j
        case let .jobRunsLoaded(job, runs):
            jobRuns[job] = runs
        case .event(let e):
            applyEvent(e)

        case .setFilter(let f):
            filter = f
        case .setPath(let p):
            path = p
        case .push(let r):
            path.append(r)
        case .pop:
            if !path.isEmpty { path.removeLast() }

        case let .draft(a, text):
            threads[a, default: ThreadState()].draft = text
        case let .replyTo(a, id):
            threads[a, default: ThreadState()].replyTo = id
        case let .sending(a, on):
            threads[a, default: ThreadState()].sending = on
        case .sent(let m):
            var t = thread(m.thread)
            t.draft = ""
            t.replyTo = nil
            t.sending = false
            threads[m.thread] = t
            upsert(m)

        case .attachOpen(let a):
            attach = AttachState(presented: true, agent: a)
        case .attachClose:
            attach = AttachState()
        case .attachAdded(let p):
            attach.pending.append(p)
        case .attachRemoved(let id):
            attach.pending.removeAll { $0.id == id }
        case let .attachUploaded(id, f):
            if let i = attach.pending.firstIndex(where: { $0.id == id }) { attach.pending[i].file = f }
        case let .attachFailed(id, msg):
            if let i = attach.pending.firstIndex(where: { $0.id == id }) { attach.pending[i].error = msg }
        case .attachNote(let n):
            attach.note = n
        case .attachSending(let on):
            attach.sending = on

        case .driveStart(let a):
            drive = DriveState(active: true, agent: a, shortAnswers: drive.shortAnswers, phase: .listening)
        case .driveTranscript(let presented):
            drive.transcriptPresented = presented
        case .driveExit:
            drive = DriveState(shortAnswers: drive.shortAnswers)
        case .driveAgent(let a):
            drive.transcriptPresented = false
            drive.agent = a
            drive.lastQuestion = nil
            drive.lastAnswer = nil
            drive.detailsSaved = false
        case .driveMuted(let m):
            drive.muted = m
            if m, drive.phase == .listening { drive.heard = "" }
        case .driveShortAnswers(let on):
            drive.shortAnswers = on
        case .drivePhase(let p):
            drive.phase = p
            if p != .listening { drive.heard = "" }
        case .driveHeard(let h):
            drive.heard = h
        case let .driveAsked(q, after):
            drive.lastQuestion = q
            drive.lastAnswer = nil
            drive.detailsSaved = false
            drive.askedAfter = after
            drive.heard = ""
            drive.phase = .thinking
        case let .driveAnswered(a, details):
            drive.lastAnswer = a
            drive.detailsSaved = details
            drive.phase = .speaking
            drive.heard = ""
        case .driveError(let e):
            drive.error = e

        case .toast(let t):
            toast = t
        case .copied(let id):
            copiedBlock = id
        case .banner(let b):
            banner = b
        }
    }

    private mutating func applyEvent(_ e: ServerEvent) {
        switch e.type {
        case "message":
            guard let m = e.message else { return }
            upsert(m)
        case "delta":
            guard let id = e.id, let a = e.thread else { return }
            threads[a, default: ThreadState()].streaming[id] = e.text ?? ""
        case "agent":
            guard let a = e.agent, let st = e.state else { return }
            setState(a, st)
        default:
            break
        }
    }

    private mutating func upsert(_ m: AppMessage) {
        var t = thread(m.thread)
        if let i = t.messages.firstIndex(where: { $0.id == m.id }) {
            t.messages[i] = m
        } else if t.loaded || !t.messages.isEmpty {
            // Unloaded threads fetch full history when opened.
            let i = t.messages.firstIndex(where: { $0.id > m.id }) ?? t.messages.endIndex
            t.messages.insert(m, at: i)
        }
        if m.status != .streaming { t.streaming[m.id] = nil }
        threads[m.thread] = t
        noteLast(m)
        if m.job != nil, m.role != .user, let i = jobs.firstIndex(where: { $0.name == m.job }) {
            if m.id >= jobs[i].lastRun.id {
                if m.id > jobs[i].lastRun.id { jobs[i].runs += 1 }
                jobs[i].lastRun = m
            }
            if var runs = jobRuns[m.job!] {
                if let k = runs.firstIndex(where: { $0.id == m.id }) { runs[k] = m } else { runs.insert(m, at: 0) }
                jobRuns[m.job!] = runs
            }
        }
    }

    /// Updates the inbox row for a message's thread if it is the newest.
    private mutating func noteLast(_ m: AppMessage) {
        if let i = agents.firstIndex(where: { $0.name == m.thread }) {
            if (agents[i].lastMessage?.id ?? 0) <= m.id {
                agents[i].lastMessage = m
                agents[i].needsYou = !m.pendingActions.isEmpty
            }
        }
    }

    private mutating func setState(_ a: String, _ st: AgentState) {
        if let i = agents.firstIndex(where: { $0.name == a }) {
            agents[i].state = st
        }
    }
}

extension AppMessage {
    /// The part of a drive-mode reply meant to be spoken: everything before
    /// a line containing only "---".
    var spokenPart: (spoken: String, hasDetails: Bool) {
        let lines = text.components(separatedBy: "\n")
        if let i = lines.firstIndex(where: { $0.trimmingCharacters(in: .whitespaces) == "---" }) {
            let spoken = lines[..<i].joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
            let rest = lines[(i + 1)...].joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
            return (spoken, !rest.isEmpty)
        }
        return (text, false)
    }
}
