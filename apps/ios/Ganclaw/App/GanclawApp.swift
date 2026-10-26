import SwiftUI

@main
struct GanclawApp: App {
    @State private var store: AppStore
    @Environment(\.scenePhase) private var scenePhase

    init() {
        let env = ProcessInfo.processInfo.environment
        var speech: SpeechEngine = LiveSpeechEngine()
        var persist = true
        #if CONTROL_SERVER
        // Test launches: fake speech and nothing saved to the Keychain.
        if env["GANCLAW_FAKE_SPEECH"] == "1" { speech = FakeSpeechEngine() }
        if env["GANCLAW_EPHEMERAL"] == "1" { persist = false }
        #endif
        _ = env
        let store = AppStore(credentials: persist ? CredentialStore.load() : nil, speech: speech, persist: persist)
        _store = State(initialValue: store)
        #if CONTROL_SERVER
        ControlServer.start(store: store)
        #endif
    }

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(store)
                .onChange(of: scenePhase) { _, phase in
                    if phase == .active, store.state.connection != .unpaired {
                        Task { await store.refresh() }
                    }
                }
        }
    }
}
