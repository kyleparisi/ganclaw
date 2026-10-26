import AVFoundation
import Foundation
import Speech

/// Listening and talking for drive mode. Live on device; a fake in tests.
@MainActor
protocol SpeechEngine: AnyObject {
    /// Called with the transcript so far; `final` once the user stops.
    var onHeard: (_ text: String, _ final: Bool) -> Void { get set }
    var onSpeakingDone: () -> Void { get set }
    var isSpeaking: Bool { get }
    func requestAccess() async -> Bool
    func startListening() throws
    func stopListening()
    func speak(_ text: String)
    func stopSpeaking()
}

/// Speech recognition with silence detection plus speech synthesis. Voice
/// processing on the input cancels the phone's own speech, so the user can
/// talk over a reply to interrupt it.
@MainActor
final class LiveSpeechEngine: NSObject, SpeechEngine, AVSpeechSynthesizerDelegate {
    var onHeard: (String, Bool) -> Void = { _, _ in }
    var onSpeakingDone: () -> Void = {}
    private(set) var isSpeaking = false

    /// How long a pause ends an utterance.
    var silence: TimeInterval = 1.3

    private let recognizer = SFSpeechRecognizer(locale: Locale(identifier: "en-US"))
    private let audio = AVAudioEngine()
    private let synth = AVSpeechSynthesizer()
    private var request: SFSpeechAudioBufferRecognitionRequest?
    private var task: SFSpeechRecognitionTask?
    private var silenceTimer: Timer?
    private var latest = ""

    override init() {
        super.init()
        synth.delegate = self
    }

    func requestAccess() async -> Bool {
        let speech = await withCheckedContinuation { c in
            SFSpeechRecognizer.requestAuthorization { c.resume(returning: $0 == .authorized) }
        }
        let mic = await AVAudioApplication.requestRecordPermission()
        return speech && mic
    }

    func startListening() throws {
        guard task == nil else { return }
        guard let recognizer, recognizer.isAvailable else {
            throw NSError(domain: "Speech", code: 1, userInfo: [NSLocalizedDescriptionKey: "Speech recognition isn't available right now."])
        }
        let session = AVAudioSession.sharedInstance()
        try session.setCategory(.playAndRecord, mode: .voiceChat, options: [.allowBluetooth, .allowBluetoothA2DP, .defaultToSpeaker, .duckOthers])
        try session.setActive(true, options: .notifyOthersOnDeactivation)

        let input = audio.inputNode
        try? input.setVoiceProcessingEnabled(true)
        let req = SFSpeechAudioBufferRecognitionRequest()
        req.shouldReportPartialResults = true
        if recognizer.supportsOnDeviceRecognition { req.requiresOnDeviceRecognition = true }
        request = req
        latest = ""
        input.installTap(onBus: 0, bufferSize: 1024, format: input.outputFormat(forBus: 0)) { @Sendable [weak req] buf, _ in
            req?.append(buf)
        }
        audio.prepare()
        try audio.start()
        task = recognizer.recognitionTask(with: req) { @Sendable [weak self] result, error in
            Task { @MainActor in self?.handle(result: result, error: error) }
        }
    }

    private func handle(result: SFSpeechRecognitionResult?, error: Error?) {
        if let result {
            let text = result.bestTranscription.formattedString
            guard text != latest else { return }
            latest = text
            if isSpeaking, !text.isEmpty { stopSpeaking() } // barge-in
            onHeard(text, false)
            silenceTimer?.invalidate()
            silenceTimer = Timer.scheduledTimer(withTimeInterval: silence, repeats: false) { @Sendable [weak self] _ in
                Task { @MainActor in self?.finishUtterance() }
            }
        } else if error != nil {
            restart()
        }
    }

    private func finishUtterance() {
        let text = latest.trimmingCharacters(in: .whitespacesAndNewlines)
        restart()
        if !text.isEmpty { onHeard(text, true) }
    }

    /// Recognition tasks end after an utterance; start a fresh one.
    private func restart() {
        stopListening()
        try? startListening()
    }

    func stopListening() {
        silenceTimer?.invalidate()
        silenceTimer = nil
        task?.cancel()
        task = nil
        request?.endAudio()
        request = nil
        if audio.isRunning {
            audio.stop()
            audio.inputNode.removeTap(onBus: 0)
        }
        latest = ""
    }

    func speak(_ text: String) {
        stopSpeaking()
        let u = AVSpeechUtterance(string: text)
        u.rate = AVSpeechUtteranceDefaultSpeechRate * 1.05
        isSpeaking = true
        synth.speak(u)
    }

    func stopSpeaking() {
        guard synth.isSpeaking else { return }
        synth.stopSpeaking(at: .immediate)
    }

    nonisolated func speechSynthesizer(_ s: AVSpeechSynthesizer, didFinish u: AVSpeechUtterance) {
        Task { @MainActor in self.finishedSpeaking() }
    }

    nonisolated func speechSynthesizer(_ s: AVSpeechSynthesizer, didCancel u: AVSpeechUtterance) {
        Task { @MainActor in self.finishedSpeaking() }
    }

    private func finishedSpeaking() {
        isSpeaking = false
        onSpeakingDone()
    }
}

/// Drive mode without a microphone or speaker. Tests feed it what the user
/// "says" and read back what the app spoke.
@MainActor
final class FakeSpeechEngine: SpeechEngine {
    var onHeard: (String, Bool) -> Void = { _, _ in }
    var onSpeakingDone: () -> Void = {}
    private(set) var isSpeaking = false
    private(set) var listening = false
    private(set) var spoken: [String] = []

    func requestAccess() async -> Bool { true }
    func startListening() throws { listening = true }
    func stopListening() { listening = false }

    func speak(_ text: String) {
        spoken.append(text)
        isSpeaking = true
        // Finish "speaking" after a beat so tests can observe the phase.
        Task { @MainActor in
            try? await Task.sleep(nanoseconds: 300_000_000)
            if self.isSpeaking { self.isSpeaking = false; self.onSpeakingDone() }
        }
    }

    func stopSpeaking() {
        guard isSpeaking else { return }
        isSpeaking = false
        onSpeakingDone()
    }

    /// Simulates the user speaking `text`.
    func hear(_ text: String, final: Bool = true) {
        guard listening else { return }
        if isSpeaking, !text.isEmpty { stopSpeaking() }
        onHeard(text, final)
    }
}
