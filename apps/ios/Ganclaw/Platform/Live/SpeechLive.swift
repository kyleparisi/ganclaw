import AVFoundation
import Foundation
@preconcurrency import Speech

extension SpeechClient {
    static func live(check: CapabilityCheck) -> SpeechClient {
        let engine = LiveSpeech(check: check)
        return SpeechClient(
            requestAccess: { try await engine.requestAccess() },
            listen: { try engine.listen($0) },
            stopListening: { engine.stopListening() },
            speak: { engine.speak($0, done: $1) },
            stopSpeaking: { engine.stopSpeaking() },
            isSpeaking: { engine.isSpeaking }
        )
    }
}

/// Speech recognition with silence detection, plus speech synthesis.
///
/// Every callback from Speech and AVFoundation arrives on a background
/// queue, so each is a @Sendable closure that hops to the main actor
/// explicitly; none may inherit this class's isolation.
@MainActor
private final class LiveSpeech: NSObject, AVSpeechSynthesizerDelegate {
    private let check: CapabilityCheck
    private let recognizer = SFSpeechRecognizer(locale: Locale(identifier: "en-US"))
    private let audio = AVAudioEngine()
    private let synth = AVSpeechSynthesizer()
    private var request: SFSpeechAudioBufferRecognitionRequest?
    private var task: SFSpeechRecognitionTask?
    private var silenceTimer: Timer?
    private var latest = ""
    private var onHeard: ((String, Bool) -> Void)?
    private var speakingDone: (() -> Void)?
    private(set) var isSpeaking = false

    /// How long a pause ends an utterance.
    private let silence: TimeInterval = 1.3

    init(check: CapabilityCheck) {
        self.check = check
        super.init()
        synth.delegate = self
    }

    func requestAccess() async throws {
        try check.require(.speechRecognition) // also requires .microphone
        guard await Self.speechAuthorized() else { throw PlatformError.denied(.speechRecognition) }
        guard await AVAudioApplication.requestRecordPermission() else { throw PlatformError.denied(.microphone) }
    }

    private nonisolated static func speechAuthorized() async -> Bool {
        if SFSpeechRecognizer.authorizationStatus() == .authorized { return true }
        return await withCheckedContinuation { (c: CheckedContinuation<Bool, Never>) in
            SFSpeechRecognizer.requestAuthorization { @Sendable status in
                c.resume(returning: status == .authorized)
            }
        }
    }

    func listen(_ onHeard: @escaping (String, Bool) -> Void) throws {
        try check.require(.speechRecognition)
        self.onHeard = onHeard
        try startRecognition()
    }

    private func startRecognition() throws {
        guard task == nil else { return }
        guard let recognizer, recognizer.isAvailable else {
            throw PlatformError.unavailable("Speech recognition isn't available right now.")
        }
        let session = AVAudioSession.sharedInstance()
        #if targetEnvironment(macCatalyst)
        try session.setCategory(.playAndRecord, mode: .default, options: [.defaultToSpeaker])
        #else
        try session.setCategory(.playAndRecord, mode: .voiceChat, options: [.allowBluetooth, .allowBluetoothA2DP, .defaultToSpeaker, .duckOthers])
        #endif
        try session.setActive(true, options: .notifyOthersOnDeactivation)

        let input = audio.inputNode
        #if !targetEnvironment(macCatalyst)
        // Echo cancellation lets the user talk over a spoken reply. On the
        // Mac it changes the input format under the engine, and a tap that
        // no longer matches the hardware raises an Objective-C exception
        // Swift can't catch, so the Mac relies on headphones instead.
        try? input.setVoiceProcessingEnabled(true)
        #endif
        let hardware = input.inputFormat(forBus: 0)
        guard hardware.sampleRate > 0, hardware.channelCount > 0 else {
            throw PlatformError.unavailable("No microphone is available.")
        }
        input.removeTap(onBus: 0) // a failed earlier start may have left one
        let req = SFSpeechAudioBufferRecognitionRequest()
        req.shouldReportPartialResults = true
        if recognizer.supportsOnDeviceRecognition { req.requiresOnDeviceRecognition = true }
        // nil format: the tap takes the bus's own current format, which is
        // always valid for it.
        input.installTap(onBus: 0, bufferSize: 1024, format: nil) { @Sendable [weak req] buf, _ in
            req?.append(buf)
        }
        audio.prepare()
        do {
            try audio.start()
        } catch {
            input.removeTap(onBus: 0)
            throw error
        }
        request = req
        latest = ""
        task = recognizer.recognitionTask(with: req) { @Sendable [weak self] result, error in
            let text = result?.bestTranscription.formattedString
            let failed = result == nil && error != nil
            Task { @MainActor in self?.handle(text: text, failed: failed) }
        }
    }

    private func handle(text: String?, failed: Bool) {
        if let text {
            guard text != latest else { return }
            latest = text
            if isSpeaking, !text.isEmpty { stopSpeaking() } // barge-in
            onHeard?(text, false)
            silenceTimer?.invalidate()
            silenceTimer = Timer.scheduledTimer(withTimeInterval: silence, repeats: false) { @Sendable [weak self] _ in
                Task { @MainActor in self?.finishUtterance() }
            }
        } else if failed {
            restart()
        }
    }

    private func finishUtterance() {
        let text = latest.trimmingCharacters(in: .whitespacesAndNewlines)
        restart()
        if !text.isEmpty { onHeard?(text, true) }
    }

    /// Recognition tasks end after an utterance; start a fresh one.
    private func restart() {
        stopRecognition()
        try? startRecognition()
    }

    func stopListening() {
        onHeard = nil
        stopRecognition()
    }

    private func stopRecognition() {
        silenceTimer?.invalidate()
        silenceTimer = nil
        task?.cancel()
        task = nil
        request?.endAudio()
        request = nil
        if audio.isRunning { audio.stop() }
        audio.inputNode.removeTap(onBus: 0)
        latest = ""
    }

    func speak(_ text: String, done: @escaping () -> Void) {
        stopSpeaking()
        let u = AVSpeechUtterance(string: text)
        u.rate = AVSpeechUtteranceDefaultSpeechRate * 1.05
        isSpeaking = true
        speakingDone = done
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
        let done = speakingDone
        speakingDone = nil
        done?()
    }
}
