import SwiftUI

struct SettingsView: View {
    @Environment(AppStore.self) private var store

    var body: some View {
        Form {
            Section {
                CButton("settings.voice") { store.send(.push(.voiceSettings)) } label: {
                    HStack {
                        Label("Voice", systemImage: "waveform")
                        Spacer()
                        Image(systemName: "chevron.right").foregroundStyle(Theme.muted)
                    }
                    .foregroundStyle(Theme.ink)
                }
            } header: { Text("Audio") }
            Section {
                if let name = store.state.deviceName { LabeledContent("Device", value: name) }
                if let server = store.state.server { LabeledContent("Server", value: server.host ?? server.absoluteString) }
                CButton("settings.refresh") { Task { await store.refresh() } } label: { Text("Refresh") }
                CButton("settings.unpair") { store.unpair() } label: {
                    Text("Unpair this phone").foregroundStyle(Theme.red)
                }
            } header: { Text("Connection") }
        }
        .scrollContentBackground(.hidden)
        .background(Theme.ground)
        .navigationTitle("Settings")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
    }
}

struct VoiceSettingsView: View {
    @Environment(AppStore.self) private var store
    @Environment(\.scenePhase) private var scenePhase
    @State private var voices: [SpeechVoice] = []

    private var preferences: SpeechPreferences { store.state.speechPreferences }

    var body: some View {
        Form {
            Section {
                LabeledContent("Speaking speed", value: String(format: "%.2f×", preferences.speed))
                Slider(value: Binding(get: { preferences.speed }, set: {
                    var value = preferences
                    value.speed = $0
                    store.setSpeechPreferences(value)
                }), in: SpeechPreferences.speeds, step: 0.05)
                .accessibilityLabel("Speaking speed")
                .control("voice.speed")
                CButton("voice.preview") { store.previewVoice() } label: {
                    Label("Play sample", systemImage: "play.fill")
                }
                CButton("voice.stop") { store.platform.speech.stopSpeaking() } label: {
                    Label("Stop sample", systemImage: "stop.fill")
                }
            }
            Section {
                row(id: "", title: "System default")
                if !preferences.voiceID.isEmpty, !voices.contains(where: { $0.id == preferences.voiceID }) {
                    row(id: preferences.voiceID, title: "Unavailable voice (using system default)")
                }
                ForEach(voices) { voice in row(id: voice.id, title: voice.name, detail: voice.detail) }
            } header: {
                Text("Voice")
            } footer: {
                Text("Tap a voice to hear it. Enhanced and Premium voices sound much better; download them in Settings › Accessibility › Spoken Content › Voices, then come back here.")
            }
        }
        .scrollContentBackground(.hidden)
        .background(Theme.ground)
        .navigationTitle("Voice")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        .onAppear { voices = store.platform.speech.voices() }
        // Voices are downloaded in the Settings app: pick up new ones on return.
        .onChange(of: scenePhase) { _, phase in
            if phase == .active { voices = store.platform.speech.voices() }
        }
        .onDisappear { store.platform.speech.stopSpeaking() }
    }

    private func row(id: String, title: String, detail: String = "") -> some View {
        CButton("voice.option.\(id.isEmpty ? "default" : id)") { store.chooseVoice(id) } label: {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text(title).foregroundStyle(Theme.ink)
                    if !detail.isEmpty { Text(detail).font(.caption).foregroundStyle(Theme.muted) }
                }
                Spacer()
                if preferences.voiceID == id { Image(systemName: "checkmark").foregroundStyle(Theme.blue) }
            }
            .contentShape(Rectangle())
        }
    }
}
