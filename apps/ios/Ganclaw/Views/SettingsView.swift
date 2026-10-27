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
    @State private var voices: [SpeechVoice] = []

    private var preferences: SpeechPreferences { store.state.speechPreferences }

    var body: some View {
        Form {
            Section {
                Picker("Voice", selection: Binding(get: { preferences.voiceID }, set: {
                    var value = preferences
                    value.voiceID = $0
                    store.setSpeechPreferences(value)
                })) {
                    Text("System default").tag("")
                    if !preferences.voiceID.isEmpty, !voices.contains(where: { $0.id == preferences.voiceID }) {
                        Text("Unavailable voice (using system default)").tag(preferences.voiceID)
                    }
                    ForEach(voices) { voice in Text(voice.title).tag(voice.id) }
                }
                .control("voice.picker")
            } footer: {
                Text("Voices available on this device. You can download more voices in the system Accessibility settings. Your choice applies to spoken replies on this device.")
            }
            Section {
                LabeledContent("Speaking speed", value: String(format: "%.2f×", preferences.speed))
                Slider(value: Binding(get: { preferences.speed }, set: {
                    var value = preferences
                    value.speed = $0
                    store.setSpeechPreferences(value)
                }), in: 0.5...1.5, step: 0.05)
                .accessibilityLabel("Speaking speed")
                .control("voice.speed")
                CButton("voice.preview") { store.previewVoice() } label: {
                    Label("Play sample", systemImage: "play.fill")
                }
                CButton("voice.stop") { store.platform.speech.stopSpeaking() } label: {
                    Label("Stop sample", systemImage: "stop.fill")
                }
            }
        }
        .scrollContentBackground(.hidden)
        .background(Theme.ground)
        .navigationTitle("Voice")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.visible, for: .navigationBar)
        .onAppear { voices = store.platform.speech.voices() }
        .onDisappear { store.platform.speech.stopSpeaking() }
    }
}
