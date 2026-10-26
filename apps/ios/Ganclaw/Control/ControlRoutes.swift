#if CONTROL_SERVER
import Foundation
import UIKit

/// The control API's routes. Two kinds:
///
/// - UI routes act on what is on screen, like a user: /ui-tree lists the
///   accessibility elements, /tap activates one by identifier or label,
///   /screenshot captures the window.
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
            return .json(["screen": store.state.currentScreen, "elements": AccessibilityTree.snapshot().map(\.json)])
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
        let elements = AccessibilityTree.snapshot()
        guard let el = elements.first(where: { e in
            if let id { return e.identifier == id }
            return e.label == label
        }) else {
            return .json(["ok": false, "error": "no element \(id ?? label!)", "screen": store.state.currentScreen,
                          "identifiers": elements.compactMap(\.identifier).filter { !$0.isEmpty }], status: 404)
        }
        let activated = el.object?.accessibilityActivate() ?? false
        return .json(["ok": activated, "element": el.json])
    }

    private func navigate(_ body: [String: Any]) async -> HTTPResponse {
        switch str(body, "route") {
        case "inbox":
            store.send(.setPath([]))
        case "thread":
            guard let agent = agent(body) else { return unknownAgent(body) }
            store.send(.setPath([]))
            store.openThread(agent)
            await store.loadThread(agent)
        case "jobs":
            store.send(.setPath([.jobs]))
        case "job":
            let job = str(body, "job")
            store.send(.setPath([.jobs]))
            store.openJob(job)
            await store.loadJobRuns(job)
        default:
            return .error("route must be inbox, thread, jobs or job")
        }
        return .ok
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

/// A flat list of the accessibility elements on screen, in reading order.
enum AccessibilityTree {
    /// SwiftUI only builds its accessibility elements while an assistive
    /// technology is attached. XCUITest turns on the system's automation
    /// flag for that; the control server does the same. Private API, which
    /// is fine here: this file is compiled into Debug builds only.
    static func enable() {
        guard let lib = dlopen("/usr/lib/libAccessibility.dylib", RTLD_NOW) else { return }
        typealias SetFlag = @convention(c) (Bool) -> Void
        for name in ["_AXSSetAutomationEnabled", "_AXSApplicationAccessibilitySetEnabled"] {
            if let sym = dlsym(lib, name) {
                unsafeBitCast(sym, to: SetFlag.self)(true)
            }
        }
    }

    struct Element {
        weak var object: NSObject?
        var identifier: String?
        var label: String?
        var value: String?
        var traits: [String]
        var frame: CGRect
        var depth: Int

        var json: [String: Any] {
            var o: [String: Any] = ["traits": traits, "depth": depth,
                                    "frame": [Int(frame.minX), Int(frame.minY), Int(frame.width), Int(frame.height)]]
            if let identifier, !identifier.isEmpty { o["id"] = identifier }
            if let label, !label.isEmpty { o["label"] = label }
            if let value, !value.isEmpty { o["value"] = value }
            return o
        }
    }

    static func snapshot() -> [Element] {
        var out: [Element] = []
        for w in windows() { walk(w, depth: 0, into: &out) }
        return out
    }

    static func windows() -> [UIWindow] {
        UIApplication.shared.connectedScenes
            .compactMap { $0 as? UIWindowScene }
            .flatMap(\.windows)
            .filter { !$0.isHidden }
    }

    private static func walk(_ obj: NSObject, depth: Int, into out: inout [Element]) {
        if let v = obj as? UIView, v.isHidden || v.alpha < 0.01 { return }
        if obj.accessibilityElementsHidden { return }
        let id = (obj as? UIAccessibilityIdentification)?.accessibilityIdentifier
        if obj.isAccessibilityElement || !(id ?? "").isEmpty {
            out.append(Element(object: obj, identifier: id, label: obj.accessibilityLabel, value: obj.accessibilityValue,
                               traits: traitNames(obj.accessibilityTraits), frame: obj.accessibilityFrame, depth: depth))
        }
        if obj.isAccessibilityElement && !(obj is UIView) { return }
        for child in children(of: obj) { walk(child, depth: depth + 1, into: &out) }
    }

    private static func children(of obj: NSObject) -> [NSObject] {
        if let els = obj.accessibilityElements as? [NSObject], !els.isEmpty { return els }
        let n = obj.accessibilityElementCount()
        if n != NSNotFound, n > 0 {
            return (0..<n).compactMap { obj.accessibilityElement(at: $0) as? NSObject }
        }
        if let v = obj as? UIView { return v.subviews }
        return []
    }

    private static func traitNames(_ t: UIAccessibilityTraits) -> [String] {
        let names: [(UIAccessibilityTraits, String)] = [
            (.button, "button"), (.header, "header"), (.selected, "selected"), (.staticText, "text"),
            (.image, "image"), (.link, "link"), (.notEnabled, "disabled"), (.searchField, "search"),
            (.updatesFrequently, "updates"), (.adjustable, "adjustable"),
        ]
        return names.filter { t.contains($0.0) }.map(\.1)
    }
}

enum Screenshot {
    static func png() -> Data? {
        guard let w = AccessibilityTree.windows().first(where: \.isKeyWindow) ?? AccessibilityTree.windows().first else { return nil }
        let r = UIGraphicsImageRenderer(bounds: w.bounds)
        let img = r.image { _ in w.drawHierarchy(in: w.bounds, afterScreenUpdates: true) }
        return img.pngData()
    }
}
#endif
