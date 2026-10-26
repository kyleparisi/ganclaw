#if CONTROL_SERVER
import Foundation
import UIKit

/// The control API's routes. Two kinds:
///
/// - UI routes act on what is on screen, like a user: /ui-tree lists the
///   controls on screen (see ControlRegistry), /tap runs one's action by
///   id or label, /screenshot captures the window.
/// - Intent routes call the same AppStore methods the views call, for
///   things the simulator can't do by tapping (photos, speech).
///
/// GET /state returns the whole AppState plus a few extras.
@MainActor
struct ControlRoutes {
    let server: ControlServer
    var store: AppStore { server.store }

    func route(_ req: HTTPRequest) async -> HTTPResponse {
        let body = req.json()
        switch (req.method, req.path) {
        case ("GET", "/state"):
            return state()
        case ("GET", "/ui-tree"):
            return .json(["screen": store.state.currentScreen, "elements": ControlRegistry.shared.visible().map(json)])
        case ("GET", "/screenshot"):
            guard let png = Screenshot.png() else { return .error("no window", status: 409) }
            return HTTPResponse(status: 200, contentType: "image/png", body: png)
        case ("POST", "/tap"):
            return tap(body)
        case ("GET", "/clipboard"):
            let text: Any = UIPasteboard.general.string.map { $0 as Any } ?? NSNull()
            return .json(["text": text])
        case ("GET", "/actions"):
            return .json(["actions": Array(store.actionLog.suffix(Int(req.query["limit"] ?? "") ?? 100))])

        case ("POST", "/pair"):
            store.send(.pairFields(server: str(body, "server"), code: str(body, "code")))
            await store.pair()
            return result(store.state.pairing.error)
        case ("POST", "/unpair"):
            store.unpair()
            return .ok
        case ("POST", "/refresh"):
            await store.refresh()
            return .ok

        case ("POST", "/navigate"):
            return await navigate(body)
        case ("POST", "/filter"):
            guard let f = InboxFilter(rawValue: str(body, "filter")) else { return .error("filter must be all, running, needsYou or jobs") }
            store.send(.setFilter(f))
            return .ok

        case ("POST", "/type"):
            guard let agent = agent(body) else { return unknownAgent(body) }
            store.send(.draft(agent, str(body, "text")))
            return .ok
        case ("POST", "/send"):
            guard let agent = agent(body) else { return unknownAgent(body) }
            if body["text"] != nil { store.send(.draft(agent, str(body, "text"))) }
            if let r = body["reply_to"] as? Int { store.send(.replyTo(agent, Int64(r))) }
            await store.sendDraft(agent, voice: body["voice"] as? Bool ?? false)
            return result(store.state.banner)
        case ("POST", "/choose"):
            guard let id = body["message_id"] as? Int, let m = findMessage(Int64(id)) else { return .error("no such message") }
            let label = str(body, "label")
            guard m.actions.contains(label) else { return .error("message \(id) has no button \(label)") }
            await store.choose(label, on: m)
            return result(store.state.banner)
        case ("POST", "/stop"):
            guard let agent = agent(body) else { return unknownAgent(body) }
            await store.stop(agent)
            return .ok
        case ("POST", "/reset"):
            guard let agent = agent(body) else { return unknownAgent(body) }
            await store.reset(agent)
            return .ok

        case ("POST", "/attach/open"):
            guard let agent = agent(body) else { return unknownAgent(body) }
            store.send(.attachOpen(agent))
            return .ok
        case ("POST", "/attach/add"):
            guard store.state.attach.presented else { return .error("the attach sheet isn't open", status: 409) }
            guard let b64 = body["base64"] as? String, let data = Data(base64Encoded: b64) else { return .error("base64 is required") }
            store.addAttachment(name: str(body, "name", default: "file"), mime: str(body, "mime", default: "application/octet-stream"), data: data)
            return .ok
        case ("POST", "/attach/note"):
            store.send(.attachNote(str(body, "text")))
            return .ok
        case ("POST", "/attach/send"):
            await store.sendAttachments()
            return result(store.state.attach.presented ? (store.state.banner ?? "not sent") : nil)
        case ("POST", "/attach/close"):
            store.send(.attachClose)
            return .ok

        case ("POST", "/drive/start"):
            await store.startDrive(body["agent"] as? String)
            return result(store.state.drive.error)
        case ("POST", "/drive/say"):
            guard let fake = store.speech as? FakeSpeechEngine else { return .error("launch with GANCLAW_FAKE_SPEECH=1 to simulate speech", status: 409) }
            fake.hear(str(body, "text"), final: body["final"] as? Bool ?? true)
            return .ok
        case ("POST", "/drive/mute"):
            store.setDriveMuted(body["muted"] as? Bool ?? true)
            return .ok
        case ("POST", "/drive/short"):
            store.send(.driveShortAnswers(body["on"] as? Bool ?? true))
            return .ok
        case ("POST", "/drive/repeat"):
            store.repeatLastAnswer()
            return .ok
        case ("POST", "/drive/exit"):
            store.exitDrive()
            return .ok

        default:
            return .error("no route \(req.method) \(req.path)", status: 404)
        }
    }

    // MARK: Helpers

    private func state() -> HTTPResponse {
        guard let data = try? JSON.encoder.encode(store.state),
              var obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return .error("encode failed", status: 500) }
        obj["screen"] = store.state.currentScreen
        obj["inbox_rows"] = store.state.inboxRows.map(\.name)
        if let fake = store.speech as? FakeSpeechEngine {
            obj["speech"] = ["spoken": fake.spoken, "listening": fake.listening, "speaking": fake.isSpeaking] as [String: Any]
        }
        return .json(obj)
    }

    private func tap(_ body: [String: Any]) -> HTTPResponse {
        let id = body["id"] as? String
        let label = body["label"] as? String
        guard id != nil || label != nil else { return .error("id or label is required") }
        let visible = ControlRegistry.shared.visible()
        guard let e = visible.first(where: { id != nil ? $0.id == id : $0.label == label }) else {
            return .json(["ok": false, "error": "no control \(id ?? label!) on screen", "screen": store.state.currentScreen,
                          "visible": visible.map(\.id)], status: 404)
        }
        guard let action = e.action else { return .json(["ok": false, "error": "\(e.id) can't be tapped"], status: 409) }
        guard e.enabled else { return .json(["ok": false, "error": "\(e.id) is disabled"], status: 409) }
        action()
        return .json(["ok": true, "element": json(e)])
    }

    private func json(_ e: ControlRegistry.Entry) -> [String: Any] {
        var o: [String: Any] = ["id": e.id, "enabled": e.enabled, "tappable": e.action != nil,
                                "frame": [Int(e.frame.minX), Int(e.frame.minY), Int(e.frame.width), Int(e.frame.height)]]
        if let l = e.label { o["label"] = l }
        return o
    }

    private func navigate(_ body: [String: Any]) async -> HTTPResponse {
        switch str(body, "route") {
        case "inbox":
            await settle([])
        case "thread":
            guard let agent = agent(body) else { return unknownAgent(body) }
            await settle([.thread(agent)])
            await store.loadThread(agent)
        case "jobs":
            store.send(.setPath([.jobs]))
        case "job":
            let job = str(body, "job")
            await settle([.jobs, .job(job)])
            await store.loadJobRuns(job)
        default:
            return .error("route must be inbox, thread, jobs or job")
        }
        return .ok
    }

    /// Sets the navigation path in one change and waits for it to stick:
    /// while a screen is still animating away, NavigationStack can write
    /// back a shorter path, so re-apply it until it holds.
    private func settle(_ path: [Route]) async {
        for _ in 0..<5 {
            if store.state.path != path { store.send(.setPath(path)) }
            try? await Task.sleep(nanoseconds: 400_000_000)
            if store.state.path == path { return }
        }
    }

    private func agent(_ body: [String: Any]) -> String? {
        let a = str(body, "agent")
        return store.state.agent(a) != nil ? a : nil
    }

    private func unknownAgent(_ body: [String: Any]) -> HTTPResponse {
        .error("unknown agent \(str(body, "agent")); known: \(store.state.agents.map(\.name))", status: 404)
    }

    private func findMessage(_ id: Int64) -> AppMessage? {
        for t in store.state.threads.values { if let m = t.message(id) { return m } }
        for runs in store.state.jobRuns.values { if let m = runs.first(where: { $0.id == id }) { return m } }
        return nil
    }

    private func str(_ body: [String: Any], _ k: String, default d: String = "") -> String {
        (body[k] as? String) ?? d
    }

    private func result(_ error: String?) -> HTTPResponse {
        if let error { return .json(["ok": false, "error": error], status: 409) }
        return .ok
    }
}

enum Screenshot {
    static func png() -> Data? {
        let windows = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.flatMap(\.windows).filter { !$0.isHidden }
        guard let w = windows.first(where: \.isKeyWindow) ?? windows.first else { return nil }
        let r = UIGraphicsImageRenderer(bounds: w.bounds)
        let img = r.image { _ in w.drawHierarchy(in: w.bounds, afterScreenUpdates: true) }
        return img.pngData()
    }
}
#endif
