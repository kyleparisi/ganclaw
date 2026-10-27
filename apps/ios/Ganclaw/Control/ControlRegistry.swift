import SwiftUI

// Every control the tests address is marked `.control("id")`, or built
// with `CButton("id")`. Release builds just set the accessibility
// identifier. With the control server, the view also registers its id,
// label, on-screen frame and (for buttons) the same action closure the
// button runs, while it is on screen. /ui-tree lists what's registered;
// /tap runs a registered action.

extension View {
    /// Marks a view for the control server and accessibility.
    func control(_ id: String, label: String? = nil, action: (() -> Void)? = nil) -> some View {
        modifier(ControlModifier(id: id, label: label, action: action))
    }
}

/// A Button the control server can tap.
struct CButton<Label: View>: View {
    let id: String
    let action: () -> Void
    let label: Label

    init(_ id: String, action: @escaping () -> Void, @ViewBuilder label: () -> Label) {
        self.id = id
        self.action = action
        self.label = label()
    }

    var body: some View {
        Button(action: action) { label }.control(id, action: action)
    }
}

extension CButton where Label == Text {
    init(_ id: String, title: String, action: @escaping () -> Void) {
        self.init(id, action: action) { Text(title) }
    }
}

#if CONTROL_SERVER
struct ControlModifier: ViewModifier {
    let id: String
    let label: String?
    let action: (() -> Void)?
    @State private var entry = ControlRegistry.Entry()
    @Environment(\.isEnabled) private var enabled

    func body(content: Content) -> some View {
        // Refresh on every render so the action and label are current.
        entry.id = id
        entry.label = label
        entry.action = action
        entry.enabled = enabled
        return content
            .accessibilityIdentifier(id)
            .background {
                GeometryReader { p in
                    Color.clear
                        .onAppear { entry.frame = p.frame(in: .global); ControlRegistry.shared.add(entry) }
                        .onChange(of: p.frame(in: .global)) { _, f in entry.frame = f }
                        .onDisappear { ControlRegistry.shared.remove(entry) }
                }
            }
    }
}

@MainActor
final class ControlRegistry {
    static let shared = ControlRegistry()

    final class Entry {
        var id = ""
        var label: String?
        var action: (() -> Void)?
        var enabled = true
        var frame: CGRect = .zero
    }

    private var entries: [ObjectIdentifier: Entry] = [:]

    func add(_ e: Entry) { entries[ObjectIdentifier(e)] = e }
    func remove(_ e: Entry) { entries[ObjectIdentifier(e)] = nil }

    /// Registered controls, top to bottom.
    func visible() -> [Entry] {
        let screen = UIScreen.main.bounds
        return entries.values
            .filter { !$0.frame.isEmpty && $0.frame.intersects(screen) }
            .sorted { ($0.frame.minY, $0.frame.minX) < ($1.frame.minY, $1.frame.minX) }
    }
}
#else
struct ControlModifier: ViewModifier {
    let id: String
    let label: String?
    let action: (() -> Void)?

    func body(content: Content) -> some View {
        content.accessibilityIdentifier(id)
    }
}
#endif
