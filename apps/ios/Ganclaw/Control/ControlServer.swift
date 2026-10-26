#if CONTROL_SERVER
import Foundation
import Network
import UIKit

// A test-only HTTP server inside the app, like bufflehead's control API.
// It binds 127.0.0.1 on GANCLAW_CONTROL_PORT (default: a free port), and
// prints on startup:
//
//   Control server: http://127.0.0.1:<port>
//   Control key: <key>
//
// Every request needs "Authorization: Bearer <key>". The key is minted per
// launch unless GANCLAW_CONTROL_KEY pins one (the test harness does). The
// same two values are written to <tmp>/ganclaw-control.json.
//
// Routes are in ControlRoutes.swift. Only Debug builds include this file.
@MainActor
final class ControlServer {
    static private(set) var shared: ControlServer?

    let store: AppStore
    let key: String
    /// Set when launched with GANCLAW_FAKE_SPEECH=1.
    let fakeSpeech: FakeSpeech?
    private var listener: NWListener?
    private(set) var port: UInt16 = 0

    static func start(store: AppStore, fakeSpeech: FakeSpeech?) {
        let env = ProcessInfo.processInfo.environment
        let key = env["GANCLAW_CONTROL_KEY"].flatMap { $0.isEmpty ? nil : $0 } ?? Self.mintKey()
        let port = UInt16(env["GANCLAW_CONTROL_PORT"] ?? "") ?? 0
        let s = ControlServer(store: store, key: key, fakeSpeech: fakeSpeech)
        shared = s
        do {
            try s.listen(port: port)
        } catch {
            print("Control server failed: \(error)")
        }
    }

    private init(store: AppStore, key: String, fakeSpeech: FakeSpeech?) {
        self.store = store
        self.key = key
        self.fakeSpeech = fakeSpeech
    }

    private static func mintKey() -> String {
        var b = [UInt8](repeating: 0, count: 24)
        _ = SecRandomCopyBytes(kSecRandomDefault, b.count, &b)
        return b.map { String(format: "%02x", $0) }.joined()
    }

    private func listen(port: UInt16) throws {
        let params = NWParameters.tcp
        params.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: NWEndpoint.Port(rawValue: port) ?? .any)
        params.allowLocalEndpointReuse = true
        let l = try NWListener(using: params)
        l.newConnectionHandler = { [weak self] conn in
            Task { @MainActor in self?.accept(conn) }
        }
        l.stateUpdateHandler = { [weak self] state in
            Task { @MainActor in
                guard let self else { return }
                if case .ready = state, let p = l.port?.rawValue {
                    self.port = p
                    self.announce()
                }
                if case .failed(let e) = state { print("Control server failed: \(e)") }
            }
        }
        l.start(queue: .main)
        listener = l
    }

    private func announce() {
        let url = "http://127.0.0.1:\(port)"
        print("Control server: \(url)")
        print("Control key: \(key)")
        fflush(stdout)
        let info: [String: Any] = ["url": url, "port": Int(port), "key": key, "pid": Int(ProcessInfo.processInfo.processIdentifier)]
        if let data = try? JSONSerialization.data(withJSONObject: info) {
            try? data.write(to: URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent("ganclaw-control.json"))
        }
    }

    // MARK: HTTP

    private func accept(_ conn: NWConnection) {
        conn.start(queue: .main)
        receive(conn, buffer: Data())
    }

    private func receive(_ conn: NWConnection, buffer: Data) {
        conn.receive(minimumIncompleteLength: 1, maximumLength: 1 << 20) { [weak self] data, _, done, error in
            Task { @MainActor in
                guard let self else { return }
                var buf = buffer
                if let data { buf.append(data) }
                if let req = HTTPRequest.parse(buf) {
                    let resp = await self.handle(req)
                    self.reply(conn, resp)
                } else if done || error != nil || buf.count > 64 << 20 {
                    conn.cancel()
                } else {
                    self.receive(conn, buffer: buf)
                }
            }
        }
    }

    private func reply(_ conn: NWConnection, _ r: HTTPResponse) {
        var head = "HTTP/1.1 \(r.status) \(HTTPResponse.reason(r.status))\r\n"
        head += "Content-Type: \(r.contentType)\r\nContent-Length: \(r.body.count)\r\nConnection: close\r\n\r\n"
        var out = Data(head.utf8)
        out.append(r.body)
        conn.send(content: out, completion: .contentProcessed { _ in conn.cancel() })
    }

    private func handle(_ req: HTTPRequest) async -> HTTPResponse {
        let auth = req.headers["authorization"] ?? ""
        guard constantTimeEqual(auth, "Bearer \(key)") else {
            return .json(["ok": false, "error": "missing or wrong control key"], status: 401)
        }
        return await ControlRoutes(server: self).route(req)
    }

    private func constantTimeEqual(_ a: String, _ b: String) -> Bool {
        let (x, y) = (Array(a.utf8), Array(b.utf8))
        guard x.count == y.count else { return false }
        return zip(x, y).reduce(0) { $0 | ($1.0 ^ $1.1) } == 0
    }
}

struct HTTPRequest {
    var method: String
    var path: String
    var query: [String: String]
    var headers: [String: String]
    var body: Data

    /// Parses a complete request, or returns nil if more bytes are needed.
    static func parse(_ data: Data) -> HTTPRequest? {
        let sep = Data("\r\n\r\n".utf8)
        guard let end = data.range(of: sep) else { return nil }
        guard let head = String(data: data[..<end.lowerBound], encoding: .utf8) else { return nil }
        var lines = head.components(separatedBy: "\r\n")
        let start = lines.removeFirst().split(separator: " ")
        guard start.count >= 2 else { return nil }
        var headers: [String: String] = [:]
        for l in lines {
            guard let i = l.firstIndex(of: ":") else { continue }
            headers[l[..<i].lowercased()] = l[l.index(after: i)...].trimmingCharacters(in: .whitespaces)
        }
        let length = Int(headers["content-length"] ?? "0") ?? 0
        let bodyStart = end.upperBound
        guard data.count - bodyStart >= length else { return nil }
        let target = String(start[1])
        let comps = URLComponents(string: "http://x" + target)
        var query: [String: String] = [:]
        comps?.queryItems?.forEach { query[$0.name] = $0.value ?? "" }
        return HTTPRequest(method: String(start[0]), path: comps?.path ?? target, query: query, headers: headers,
                           body: data.subdata(in: bodyStart..<(bodyStart + length)))
    }

    func json() -> [String: Any] {
        guard !body.isEmpty, let o = try? JSONSerialization.jsonObject(with: body) as? [String: Any] else { return [:] }
        return o
    }
}

struct HTTPResponse {
    var status: Int
    var contentType: String
    var body: Data

    static func json(_ obj: [String: Any], status: Int = 200) -> HTTPResponse {
        let data = (try? JSONSerialization.data(withJSONObject: obj, options: [.sortedKeys])) ?? Data("{}".utf8)
        return HTTPResponse(status: status, contentType: "application/json", body: data)
    }

    static func encodable<T: Encodable>(_ v: T) -> HTTPResponse {
        HTTPResponse(status: 200, contentType: "application/json", body: (try? JSON.encoder.encode(v)) ?? Data("{}".utf8))
    }

    static func error(_ msg: String, status: Int = 400) -> HTTPResponse { json(["ok": false, "error": msg], status: status) }
    static var ok: HTTPResponse { json(["ok": true]) }

    static func reason(_ s: Int) -> String {
        switch s {
        case 200: "OK"
        case 400: "Bad Request"
        case 401: "Unauthorized"
        case 404: "Not Found"
        case 409: "Conflict"
        default: "Error"
        }
    }
}
#endif
