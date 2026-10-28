import Foundation

// Wire types for ganclaw's app channel (internal/appchannel). Field names
// follow the server's JSON; the decoder converts snake_case.

struct AppFile: Codable, Hashable, Identifiable {
    var id: String
    var name: String
    var mime: String
    var size: Int64

    var isImage: Bool { mime.hasPrefix("image/") }
    var isAudio: Bool { mime.hasPrefix("audio/") }

    /// Short type badge, e.g. "PDF", "LOG".
    var badge: String {
        let ext = (name as NSString).pathExtension.uppercased()
        if !ext.isEmpty && ext.count <= 4 { return ext }
        if isImage { return "IMG" }
        if isAudio { return "AUD" }
        return "FILE"
    }
}

enum Role: String, Codable {
    case user, agent, system
}

enum MessageStatus: String, Codable {
    case streaming, done, error
}

struct AppMessage: Codable, Hashable, Identifiable {
    var id: Int64
    var thread: String
    var role: Role
    var text: String
    var files: [AppFile]
    var actions: [String]
    var answer: String?
    var status: MessageStatus
    var job: String?
    var replyTo: Int64?
    var provider: String?
    var createdAt: Date
    var updatedAt: Date

    /// Buttons the user still has to pick from.
    var pendingActions: [String] { (answer ?? "").isEmpty ? actions : [] }

    /// Job name of work handed off from drive mode (store.BackgroundJob):
    /// its results stay in the conversation.
    static let backgroundJob = "background"

    /// A scheduled job's record: listed in Jobs, not in the conversation.
    var isScheduledJob: Bool { !(job ?? "").isEmpty && job != Self.backgroundJob }
}

struct AgentState: Codable, Hashable {
    var working: Bool
    var detail: String?
    var since: Date?

    static let idle = AgentState(working: false, detail: nil, since: nil)
}

struct ReadState: Codable, Equatable {
    var lastReadId: Int64
    var unreadCount: Int
}

struct AgentSummary: Codable, Hashable, Identifiable {
    var name: String
    var state: AgentState
    var lastMessage: AppMessage?
    var needsYou: Bool
    var lastReadId: Int64?
    var unreadCount: Int?

    var id: String { name }
}

struct JobSummary: Codable, Hashable, Identifiable {
    var name: String
    var agent: String
    var lastRun: AppMessage
    var runs: Int
    var problems: Int

    var id: String { name }
}

struct Device: Codable, Hashable {
    var id: String
    var name: String
}

/// A WebSocket event from /app/v1/events.
struct ServerEvent: Codable, Equatable {
    var read: ReadState?
    var type: String // hello | message | delta | agent
    var message: AppMessage?
    var id: Int64?
    var thread: String?
    var text: String?
    var agent: String?
    var state: AgentState?
}

struct APIErrorBody: Codable {
    struct Detail: Codable {
        var code: String
        var message: String
    }
    var error: Detail
}

enum JSON {
    static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.keyDecodingStrategy = .convertFromSnakeCase
        d.dateDecodingStrategy = .custom { decoder in
            let s = try decoder.singleValueContainer().decode(String.self)
            if let date = parseDate(s) { return date }
            throw DecodingError.dataCorrupted(.init(codingPath: decoder.codingPath, debugDescription: "bad date \(s)"))
        }
        return d
    }()

    static let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.keyEncodingStrategy = .convertToSnakeCase
        e.dateEncodingStrategy = .iso8601
        e.outputFormatting = [.sortedKeys]
        return e
    }()

    private static let fractional: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()

    private static let plain: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f
    }()

    /// Parses RFC 3339 dates, including Go's nanosecond fractions, which
    /// ISO8601DateFormatter rejects past millisecond precision.
    static func parseDate(_ s: String) -> Date? {
        if let d = fractional.date(from: s) ?? plain.date(from: s) { return d }
        guard let dot = s.firstIndex(of: ".") else { return nil }
        let rest = s[s.index(after: dot)...]
        let digits = rest.prefix(while: \.isNumber)
        let zone = rest.dropFirst(digits.count)
        let trimmed = String(s[..<dot]) + "." + String(digits.prefix(3)) + zone
        return fractional.date(from: trimmed)
    }
}

struct WorkerTask: Codable, Identifiable, Equatable {
    var id: String
    var agent: String
    var state: String
    var progress: String
    var cursor: Int64
    var updatedAt: Date
    var isActive: Bool { state == "queued" || state == "running" }
}
