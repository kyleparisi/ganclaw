import Foundation

/// Drive mode without a microphone or speaker. Tests feed it what the user
/// "says" (`hear`) and read back what the app spoke (`spoken`).
@MainActor
final class FakeSpeech {
    private(set) var spoken: [String] = []
    private(set) var listening = false
    private(set) var isSpeaking = false
    private var onHeard: ((String, Bool) -> Void)?
    private var speakingDone: (() -> Void)?

    var client: SpeechClient {
        SpeechClient(
            requestAccess: {},
            listen: { [unowned self] in onHeard = $0; listening = true },
            stopListening: { [unowned self] in listening = false; onHeard = nil },
            speak: { [unowned self] text, done in
                spoken.append(text)
                isSpeaking = true
                speakingDone = done
                // Finish "speaking" after a beat so tests can see the phase.
                Task { @MainActor [weak self] in
                    try? await Task.sleep(nanoseconds: 300_000_000)
                    self?.finishSpeaking()
                }
            },
            stopSpeaking: { [unowned self] in finishSpeaking() },
            isSpeaking: { [unowned self] in isSpeaking }
        )
    }

    /// Simulates the user speaking `text`.
    func hear(_ text: String, final: Bool = true) {
        guard listening, let onHeard else { return }
        if isSpeaking, !text.isEmpty { finishSpeaking() } // barge-in
        onHeard(text, final)
    }

    private func finishSpeaking() {
        guard isSpeaking else { return }
        isSpeaking = false
        let done = speakingDone
        speakingDone = nil
        done?()
    }
}
