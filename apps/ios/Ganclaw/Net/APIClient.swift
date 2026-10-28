import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

struct APIError: LocalizedError, Equatable {
    var status: Int
    var code: String
    var message: String

    var errorDescription: String? { message }
    var isUnauthorized: Bool { status == 401 }
}

/// Talks to one ganclaw server's app channel.
struct APIClient {
    var baseURL: URL
    var token: String?
    var session: URLSession = .shared

    func url(_ path: String, query: [URLQueryItem] = []) -> URL {
        var c = URLComponents(url: baseURL, resolvingAgainstBaseURL: false)!
        let base = c.path.hasSuffix("/") ? String(c.path.dropLast()) : c.path
        c.path = base + path
        if !query.isEmpty { c.queryItems = query }
        return c.url!
    }

    func request(_ method: String, _ path: String, query: [URLQueryItem] = []) -> URLRequest {
        var r = URLRequest(url: url(path, query: query))
        r.httpMethod = method
        r.timeoutInterval = 30
        if let token { r.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        return r
    }

    private func send<T: Decodable>(_ req: URLRequest, as: T.Type) async throws -> T {
        let (data, resp) = try await session.data(for: req)
        let status = (resp as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(status) else {
            if let body = try? JSON.decoder.decode(APIErrorBody.self, from: data) {
                throw APIError(status: status, code: body.error.code, message: body.error.message)
            }
            throw APIError(status: status, code: "failed", message: "Server returned HTTP \(status)")
        }
        return try JSON.decoder.decode(T.self, from: data)
    }

    private func sendJSON<T: Decodable, B: Encodable>(_ method: String, _ path: String, body: B, as t: T.Type) async throws -> T {
        var r = request(method, path)
        r.setValue("application/json", forHTTPHeaderField: "Content-Type")
        r.httpBody = try JSON.encoder.encode(body)
        return try await send(r, as: t)
    }

    // MARK: Endpoints

    struct PairResult: Decodable { var token: String; var device: Device }
    func pair(code: String, deviceName: String) async throws -> PairResult {
        struct Body: Encodable { var code: String; var deviceName: String }
        return try await sendJSON("POST", "/app/v1/pair", body: Body(code: code, deviceName: deviceName), as: PairResult.self)
    }

    func registerPush(_ token: String) async throws {
        struct Body: Encodable { var token: String }
        struct R: Decodable { var ok: Bool }
        _ = try await sendJSON("PUT", "/app/v1/me/push", body: Body(token: token), as: R.self)
    }

    func removePush() async throws {
        struct R: Decodable { var ok: Bool }
        _ = try await send(request("DELETE", "/app/v1/me/push"), as: R.self)
    }

    func unpair() async throws {
        struct R: Decodable { var ok: Bool }
        _ = try await send(request("DELETE", "/app/v1/me"), as: R.self)
    }

    func agents() async throws -> [AgentSummary] {
        struct R: Decodable { var agents: [AgentSummary] }
        return try await send(request("GET", "/app/v1/agents"), as: R.self).agents
    }

    func markRead(agent: String, messageID: Int64) async throws -> ReadState {
        struct Body: Encodable { var messageId: Int64 }
        return try await sendJSON("PUT", "/app/v1/threads/\(escape(agent))/read", body: Body(messageId: messageID), as: ReadState.self)
    }

    struct ThreadPage: Decodable { var messages: [AppMessage]; var state: AgentState }
    func messages(agent: String, before: Int64? = nil, limit: Int = 100) async throws -> ThreadPage {
        var q = [URLQueryItem(name: "limit", value: String(limit))]
        if let before { q.append(URLQueryItem(name: "before", value: String(before))) }
        return try await send(request("GET", "/app/v1/threads/\(escape(agent))/messages", query: q), as: ThreadPage.self)
    }

    func send(agent: String, text: String, fileIDs: [String], replyTo: Int64?, voice: Bool, driveMode: Bool = false) async throws -> AppMessage {
        struct Body: Encodable { var text: String; var fileIds: [String]?; var replyTo: Int64?; var voice: Bool?; var driveMode: Bool? }
        struct R: Decodable { var message: AppMessage }
        let body = Body(text: text, fileIds: fileIDs.isEmpty ? nil : fileIDs, replyTo: replyTo, voice: voice ? true : nil, driveMode: driveMode ? true : nil)
        return try await sendJSON("POST", "/app/v1/threads/\(escape(agent))/messages", body: body, as: R.self).message
    }

    func upload(agent: String, name: String, mime: String, data: Data) async throws -> AppFile {
        struct R: Decodable { var file: AppFile }
        var r = request("POST", "/app/v1/threads/\(escape(agent))/files", query: [URLQueryItem(name: "name", value: name)])
        r.setValue(mime, forHTTPHeaderField: "Content-Type")
        r.httpBody = data
        r.timeoutInterval = 120
        return try await send(r, as: R.self).file
    }

    func stop(agent: String) async throws {
        struct R: Decodable { var ok: Bool }
        _ = try await send(request("POST", "/app/v1/threads/\(escape(agent))/stop"), as: R.self)
    }

    func reset(agent: String) async throws {
        struct R: Decodable { var ok: Bool }
        _ = try await send(request("POST", "/app/v1/threads/\(escape(agent))/reset"), as: R.self)
    }

    func tasks(archived: Bool, before: Int64? = nil) async throws -> [WorkerTask] {
        struct R: Decodable { var tasks: [WorkerTask] }
        var query = [URLQueryItem(name: "archived", value: String(archived))]
        if let before { query.append(URLQueryItem(name: "before", value: String(before))) }
        return try await send(request("GET", "/app/v1/tasks", query: query), as: R.self).tasks
    }

    func taskMessages(_ id: String, before: Int64? = nil) async throws -> [AppMessage] {
        struct R: Decodable { var messages: [AppMessage] }
        let query = before.map { [URLQueryItem(name: "before", value: String($0))] } ?? []
        return try await send(request("GET", "/app/v1/tasks/\(escape(id))/messages", query: query), as: R.self).messages
    }

    func jobs() async throws -> [JobSummary] {
        struct R: Decodable { var jobs: [JobSummary] }
        return try await send(request("GET", "/app/v1/jobs"), as: R.self).jobs
    }

    func jobRuns(job: String) async throws -> [AppMessage] {
        struct R: Decodable { var runs: [AppMessage] }
        return try await send(request("GET", "/app/v1/jobs/\(escape(job))/runs"), as: R.self).runs
    }

    func fileURL(_ id: String) -> URL { url("/app/v1/files/\(escape(id))") }

    func download(_ file: AppFile) async throws -> Data {
        let (data, resp) = try await session.data(for: request("GET", "/app/v1/files/\(escape(file.id))"))
        let status = (resp as? HTTPURLResponse)?.statusCode ?? 0
        guard status == 200 else { throw APIError(status: status, code: "failed", message: "Couldn't download \(file.name)") }
        return data
    }

    func eventsRequest() -> URLRequest {
        var c = URLComponents(url: url("/app/v1/events"), resolvingAgainstBaseURL: false)!
        c.scheme = c.scheme == "https" ? "wss" : "ws"
        var r = URLRequest(url: c.url!)
        if let token { r.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        return r
    }

    private func escape(_ s: String) -> String {
        s.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(CharacterSet(charactersIn: "/"))) ?? s
    }
}
