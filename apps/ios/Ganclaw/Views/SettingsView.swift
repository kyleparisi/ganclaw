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
            #if !targetEnvironment(macCatalyst)
            NotificationsSection()
            #endif
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
        .inlineNavigationBar()
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
        .inlineNavigationBar()
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

/// Push status for this phone, with the one action that makes sense from
/// each state. "On" means the server has the token, not just iOS permission.
struct NotificationsSection: View {
    @Environment(AppStore.self) private var store

    var body: some View {
        #if os(iOS) && !targetEnvironment(macCatalyst)
        let status = store.state.notifications
        Section {
            switch status {
            case .on:
                HStack {
                    Label("Notifications are on", systemImage: "bell.badge.fill")
                        .foregroundStyle(Theme.ink)
                    Spacer()
                    Image(systemName: "checkmark.circle.fill").foregroundStyle(Theme.teal)
                }
                .control("settings.notifications.status", label: "on")
                systemSettingsButton("Alert style and sounds")
            case .enabling:
                HStack {
                    Label("Turning on notifications…", systemImage: "bell")
                        .foregroundStyle(Theme.muted)
                    Spacer()
                    ProgressView()
                }
                .control("settings.notifications.status", label: "enabling")
            case .denied:
                Label("Notifications are blocked", systemImage: "bell.slash")
                    .foregroundStyle(Theme.ink)
                    .control("settings.notifications.status", label: "denied")
                systemSettingsButton("Allow in Settings")
            case .failed:
                Label(store.state.notificationError ?? "Couldn't turn on notifications.", systemImage: "exclamationmark.triangle")
                    .foregroundStyle(Theme.red)
                    .control("settings.notifications.status", label: "failed")
                enableButton("Try again")
            case .off, .unknown:
                enableButton("Turn on notifications")
            }
        } header: { Text("Notifications") } footer: {
            Text(footer(status))
        }
        #else
        EmptyView()
        #endif
    }

    private func footer(_ status: NotificationStatus) -> String {
        switch status {
        case .on: "Agent replies and failed jobs alert this phone while Ganclaw is in the background."
        case .denied: "iOS is blocking alerts from Ganclaw. Turn on Allow Notifications in Settings, then come back."
        default: "Get agent replies and failed jobs while Ganclaw is in the background."
        }
    }

    private func enableButton(_ title: String) -> some View {
        CButton("settings.notifications") { Task { await store.enableNotifications() } } label: {
            Label(title, systemImage: "bell")
        }
    }

    private func systemSettingsButton(_ title: String) -> some View {
        CButton("settings.notifications.system") { store.openNotificationSettings() } label: {
            HStack {
                Text(title)
                Spacer()
                Image(systemName: "arrow.up.forward.app").foregroundStyle(Theme.muted)
            }
        }
    }
}
