import SwiftUI

/// Ganclaw › Settings… (⌘,). Uses system colors and controls so it follows
/// the Mac's light or dark appearance, unlike the main window's Relay theme.
struct MacSettingsView: View {
    @Environment(AppStore.self) private var store

    var body: some View {
        TabView {
            MacConnectionSettings()
                .tabItem { Label("Connection", systemImage: "network") }
            MacVoiceSettings()
                .tabItem { Label("Voice", systemImage: "waveform") }
        }
        .frame(width: 500)
        .fixedSize(horizontal: false, vertical: true)
    }
}

private struct MacConnectionSettings: View {
    @Environment(AppStore.self) private var store
    @State private var confirmUnpair = false

    var body: some View {
        let s = store.state
        Form {
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
                        .foregroundStyle(.secondary)
                }
            }
        }
        .formStyle(.grouped)
        .scrollDisabled(true)
        .confirmationDialog("Unpair this Mac?", isPresented: $confirmUnpair) {
            Button("Unpair", role: .destructive) { store.unpair() }
        } message: {
            Text("You'll need a new pairing code to reconnect.")
        }
    }
}

/// Drive mode's spoken replies: speed and voice, with a sample to hear them.
private struct MacVoiceSettings: View {
    @Environment(AppStore.self) private var store
    @State private var voices: [SpeechVoice] = []

    private var preferences: SpeechPreferences { store.state.speechPreferences }

    var body: some View {
        Form {
            Section {
                Picker("Voice", selection: Binding(get: { preferences.voiceID }, set: { store.chooseVoice($0) })) {
                    Text("System default").tag("")
                    if !preferences.voiceID.isEmpty, !voices.contains(where: { $0.id == preferences.voiceID }) {
                        Text("Unavailable voice").tag(preferences.voiceID)
                    }
                    ForEach(Dictionary(grouping: voices, by: \.language).sorted(by: { $0.key < $1.key }), id: \.key) { language, group in
                        Section(language) {
                            ForEach(group) { v in
                                Text(v.quality.isEmpty ? v.name : "\(v.name) (\(v.quality))").tag(v.id)
                            }
                        }
                    }
                }
                LabeledContent("Speaking speed") {
                    HStack(spacing: 10) {
                        Slider(value: Binding(get: { preferences.speed }, set: {
                            var value = preferences
                            value.speed = $0
                            store.setSpeechPreferences(value)
                        }), in: SpeechPreferences.speeds, step: 0.05) {
                            Text("Speaking speed")
                        } minimumValueLabel: {
                            Image(systemName: "tortoise").foregroundStyle(.secondary)
                        } maximumValueLabel: {
                            Image(systemName: "hare").foregroundStyle(.secondary)
                        }
                        .labelsHidden()
                        .frame(width: 200)
                        Text(String(format: "%.2f×", preferences.speed))
                            .monospacedDigit()
                            .foregroundStyle(.secondary)
                            .frame(width: 44, alignment: .trailing)
                    }
                }
                HStack {
                    Spacer()
                    Button { store.platform.speech.stopSpeaking() } label: { Label("Stop", systemImage: "stop.fill") }
                    Button { store.previewVoice() } label: { Label("Play Sample", systemImage: "play.fill") }
                }
            } footer: {
                Text("Used for replies in drive mode. Enhanced and Premium voices sound much better: download them in System Settings › Accessibility › Spoken Content › System Voice › Manage Voices.")
                    .foregroundStyle(.secondary)
            }
        }
        .formStyle(.grouped)
        .scrollDisabled(true)
        .onAppear { voices = store.platform.speech.voices() }
        .onDisappear { store.platform.speech.stopSpeaking() }
    }
}
