import SwiftUI

@main
struct GanclawApp: App {
    @State private var store: AppStore
    @Environment(\.scenePhase) private var scenePhase

    init() {
        var platform = Platform.live()
        #if CONTROL_SERVER
        // Test launches swap clients, the way investor-be's tests build
        // State with stub clients: fake speech, nothing kept in the Keychain.
        let env = ProcessInfo.processInfo.environment
        var fake: FakeSpeech?
        if env["GANCLAW_FAKE_SPEECH"] == "1" {
            let f = FakeSpeech()
            platform.speech = f.client
            fake = f
        }
        if env["GANCLAW_EPHEMERAL"] == "1" { platform.keychain = .inert }
        #endif
        let store = AppStore(platform: platform)
        _store = State(initialValue: store)
        #if CONTROL_SERVER
        ControlServer.start(store: store, fakeSpeech: fake)
        #endif
    }

    var body: some Scene {
        WindowGroup {
            RootView()
                .environment(store)
                .onAppear { MacWindow.configure() }
                .onChange(of: scenePhase) { _, phase in
                    if phase == .active, store.state.connection != .unpaired {
                        Task { await store.refresh() }
                    }
                }
        }
    }
}
