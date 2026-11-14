import SwiftUI

/// The native macOS app. It shares the iOS app's state, networking and
/// platform layers (Ganclaw/Model, Net, Platform) and most message views,
/// and has its own window: a sidebar of agents beside the open thread,
/// menu commands, and a Settings window.
@main
struct GanclawMacApp: App {
    @State private var store = AppStore(platform: .live())
    @Environment(\.scenePhase) private var scenePhase

    var body: some Scene {
        Window("Ganclaw", id: "main") {
            MacRootView()
                .environment(store)
                .frame(minWidth: 760, minHeight: 520)
                .onChange(of: scenePhase) { _, phase in
                    if phase == .active, store.state.connection != .unpaired {
                        Task { await store.refresh() }
                    }
                }
                .onOpenURL { url in
                    if let link = PairLink(url) { Task { await store.open(link: link) } }
                }
        }
        .defaultSize(width: 1100, height: 760)
        .commands { MacCommands(store: store) }

        Settings {
            MacSettingsView()
                .environment(store)
        }
    }
}
