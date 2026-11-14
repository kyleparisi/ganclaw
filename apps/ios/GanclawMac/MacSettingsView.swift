import SwiftUI

/// Ganclaw › Settings… (⌘,).
struct MacSettingsView: View {
    @Environment(AppStore.self) private var store

    var body: some View {
        TabView {
            connection
                .tabItem { Label("Connection", systemImage: "network") }
            VoiceSettingsView()
                .formStyle(.grouped)
                .tabItem { Label("Voice", systemImage: "waveform") }
        }
        .frame(width: 520, height: 460)
    }

    private var connection: some View {
        let s = store.state
        return Form {
            if s.connection == .unpaired {
                Text("Not paired. Pair this Mac from the main window.").foregroundStyle(.secondary)
            } else {
                Section {
                    if let name = s.deviceName { LabeledContent("Device", value: name) }
                    if let server = s.server { LabeledContent("Server", value: server.host ?? server.absoluteString) }
                    LabeledContent("Status", value: s.connection.rawValue.capitalized)
                }
                Section {
                    HStack {
                        Button("Refresh") { Task { await store.refresh() } }
                        Spacer()
                        Button("Unpair This Mac…", role: .destructive) { confirmUnpair = true }
                    }
                } footer: {
                    Text("Unpairing removes this Mac's access. Run `ganclaw app pair` on the server for a new code.")
                }
            }
        }
        .formStyle(.grouped)
        .confirmationDialog("Unpair this Mac?", isPresented: $confirmUnpair) {
            Button("Unpair", role: .destructive) { store.unpair() }
        } message: {
            Text("You'll need a new pairing code to reconnect.")
        }
    }

    @State private var confirmUnpair = false
}
