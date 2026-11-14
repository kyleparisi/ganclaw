import SwiftUI

extension AppStore {
    /// The agent whose thread is open, if any.
    var currentAgent: String? {
        if case .thread(let a)? = state.path.last { return a }
        return nil
    }

    /// Shows a sidebar destination. The Mac keeps one page open at a time,
    /// so selecting replaces the path instead of pushing onto it (a job's
    /// runs sit under Scheduled jobs so its back button returns there).
    func select(_ route: Route) {
        switch route {
        case .thread(let a):
            if state.path != [route] { send(.setPath([route])) }
            Task { await loadThread(a) }
        case .job(let j):
            send(.setPath([.jobs, route]))
            Task { await loadJobRuns(j) }
        default:
            send(.setPath([route]))
        }
    }
}

/// The menu bar: an Agent menu for the open thread, and ⌘1–⌘9 to jump
/// between agents in sidebar order.
struct MacCommands: Commands {
    let store: AppStore

    var body: some Commands {
        // One window: no File › New Window.
        CommandGroup(replacing: .newItem) {}

        CommandMenu("Agent") {
            let paired = store.state.connection != .unpaired
            let agent = store.currentAgent
            let working = agent.flatMap { store.state.agent($0)?.state.working } ?? false

            Button("Refresh") { Task { await store.refresh() } }
                .keyboardShortcut("r")
                .disabled(!paired)
            Divider()
            Button("Attach Files…") { if let agent { store.send(.attachOpen(agent)) } }
                .keyboardShortcut("a", modifiers: [.command, .shift])
                .disabled(agent == nil)
            Button("Drive Mode") { Task { await store.startDrive(agent) } }
                .keyboardShortcut("d", modifiers: [.command, .shift])
                .disabled(!paired || store.state.agents.isEmpty)
            Button("Stop") { if let agent { Task { await store.stop(agent) } } }
                .keyboardShortcut(".")
                .disabled(!working)
            Button("New Conversation") { if let agent { Task { await store.reset(agent) } } }
                .keyboardShortcut("n", modifiers: [.command, .shift])
                .disabled(agent == nil)
            Divider()
            Button("Open Items") { store.select(.openItems) }
                .keyboardShortcut("0")
                .disabled(!paired)
            ForEach(Array(store.state.sortedAgents.prefix(9).enumerated()), id: \.element.id) { i, a in
                Button(Theme.displayName(a.name)) { store.select(.thread(a.name)) }
                    .keyboardShortcut(KeyEquivalent(Character(String(i + 1))))
            }
        }
    }
}
