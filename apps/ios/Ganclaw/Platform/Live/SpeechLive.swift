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
    private var retryTimer: Timer?
    private var configObserver: NSObjectProtocol?
    private let sink = BufferSink()
    private var generation = 0
    private var taskStarted = Date()
    private var restarts = RecognitionRestarts()
    private var latest = ""
    private var onHeard: ((String, Bool) -> Void)?
    private var speakingDone: (() -> Void)?
    private var utterance: ObjectIdentifier?
    private(set) var isSpeaking = false

    /// How long a pause ends an utterance.
    private let silence: TimeInterval = 1.3

    /// With echo cancellation (iOS voice processing) the mic keeps
    /// listening while a reply plays, so the user can talk over it. Without
    /// it (the Mac) the mic would hear the reply itself and cut it off, so
    /// recognition pauses while speaking and resumes after: turn-taking.
    #if targetEnvironment(macCatalyst)
    private let echoCancelled = false
    #else
    private let echoCancelled = true
    #endif
    /// Lets the reply's tail clear the room before listening again.
    private let afterSpeaking: TimeInterval = 0.4

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
        restarts = RecognitionRestarts()
        try startAudio()
        do {
            try startTask()
        } catch {
            stopAudio()
            throw error
        }
    }

    func stopListening() {
        onHeard = nil
        stopTask()
        stopAudio()
    }

    // MARK: Audio: runs for the whole listening session

    /// Starts the mic once. Recognition tasks come and go (one per
    /// utterance) without touching it: stopping and restarting the engine
    /// toggles the system's mic indicator and drops audio between tasks.
    private func startAudio() throws {
        guard !audio.isRunning else { return }
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
        // nil format: the tap takes the bus's own current format, which is
        // always valid for it. Buffers go to whichever task is current.
        let sink = self.sink
        input.installTap(onBus: 0, bufferSize: 1024, format: nil) { @Sendable buf, _ in
            sink.append(buf)
        }
        audio.prepare()
        do {
            try audio.start()
        } catch {
            input.removeTap(onBus: 0)
            throw error
        }
        configObserver = NotificationCenter.default.addObserver(
            forName: .AVAudioEngineConfigurationChange, object: audio, queue: nil
        ) { @Sendable [weak self] _ in
            Task { @MainActor in self?.audioConfigurationChanged() }
        }
    }

    private func stopAudio() {
        if let configObserver { NotificationCenter.default.removeObserver(configObserver) }
        configObserver = nil
        if audio.isRunning { audio.stop() }
        audio.inputNode.removeTap(onBus: 0)
    }

    /// The input device changed (headphones, another app, speech output):
    /// the engine has stopped. Start it again with the new format.
    private func audioConfigurationChanged() {
        guard onHeard != nil else { return }
        stopAudio()
        do { try startAudio() } catch { scheduleTask(after: 1) }
    }

    // MARK: Recognition: one task per utterance

    private func startTask() throws {
        guard task == nil else { return }
        guard let recognizer, recognizer.isAvailable else {
            throw PlatformError.unavailable("Speech recognition isn't available right now.")
        }
        let req = SFSpeechAudioBufferRecognitionRequest()
        req.shouldReportPartialResults = true
        if restarts.onDeviceAllowed, recognizer.supportsOnDeviceRecognition { req.requiresOnDeviceRecognition = true }
        generation += 1
        let gen = generation
        latest = ""
        taskStarted = Date()
        request = req
        sink.request = req
        task = recognizer.recognitionTask(with: req) { @Sendable [weak self] result, error in
            let text = result?.bestTranscription.formattedString
            let ended = error != nil || (result?.isFinal ?? false)
            Task { @MainActor in self?.handle(gen, text: text, ended: ended, failed: error != nil) }
        }
    }

    /// Stops the current task. Its callbacks may still arrive (a cancelled
    /// task reports an error); the generation check drops them, otherwise
    /// each cancel would restart the task that replaced it, forever.
    private func stopTask() {
        generation += 1
        silenceTimer?.invalidate()
        silenceTimer = nil
        retryTimer?.invalidate()
        retryTimer = nil
        sink.request = nil
        task?.cancel()
        task = nil
        request?.endAudio()
        request = nil
        latest = ""
    }

    private func handle(_ gen: Int, text: String?, ended: Bool, failed: Bool) {
        guard gen == generation, onHeard != nil else { return }
        if let text, text != latest {
            latest = text
            restarts.heard()
            if isSpeaking, !text.isEmpty { stopSpeaking() } // barge-in
            onHeard?(text, false)
            silenceTimer?.invalidate()
            silenceTimer = Timer.scheduledTimer(withTimeInterval: silence, repeats: false) { @Sendable [weak self] _ in
                Task { @MainActor in self?.finishUtterance() }
            }
        }
        if ended {
            if !latest.isEmpty {
                finishUtterance()
            } else {
                let delay = restarts.ended(after: Date().timeIntervalSince(taskStarted), failed: failed)
                stopTask()
                scheduleTask(after: delay)
            }
        }
    }

    private func finishUtterance() {
        let text = latest.trimmingCharacters(in: .whitespacesAndNewlines)
        _ = restarts.ended(after: .infinity, failed: false)
        stopTask()
        scheduleTask(after: 0)
        if !text.isEmpty { onHeard?(text, true) }
    }

    private func scheduleTask(after delay: TimeInterval) {
        retryTimer?.invalidate()
        retryTimer = nil
        guard onHeard != nil else { return }
        if isSpeaking, !echoCancelled { return } // resumes in finishedSpeaking
        if delay <= 0 {
            if (try? startTask()) == nil { scheduleTask(after: 1) }
            return
        }
        retryTimer = Timer.scheduledTimer(withTimeInterval: delay, repeats: false) { @Sendable [weak self] _ in
            Task { @MainActor in self?.scheduleTask(after: 0) }
        }
    }

    func speak(_ text: String, done: @escaping () -> Void) {
        stopSpeaking()
        let u = AVSpeechUtterance(string: text)
        u.rate = AVSpeechUtteranceDefaultSpeechRate * 1.05
        isSpeaking = true
        speakingDone = done
        utterance = ObjectIdentifier(u)
        if !echoCancelled { stopTask() } // the mic stays on; recognition waits
        synth.speak(u)
    }

    func stopSpeaking() {
        guard synth.isSpeaking else { return }
        synth.stopSpeaking(at: .immediate)
    }

    nonisolated func speechSynthesizer(_ s: AVSpeechSynthesizer, didFinish u: AVSpeechUtterance) {
        let id = ObjectIdentifier(u)
        Task { @MainActor in self.finishedSpeaking(id) }
    }

    nonisolated func speechSynthesizer(_ s: AVSpeechSynthesizer, didCancel u: AVSpeechUtterance) {
        let id = ObjectIdentifier(u)
        Task { @MainActor in self.finishedSpeaking(id) }
    }

    /// Ignores the end of an utterance a newer one replaced.
    private func finishedSpeaking(_ id: ObjectIdentifier) {
        guard id == utterance else { return }
        utterance = nil
        isSpeaking = false
        if !echoCancelled, onHeard != nil, task == nil { scheduleTask(after: afterSpeaking) }
        let done = speakingDone
        speakingDone = nil
        done?()
    }
}

/// Hands mic buffers (audio thread) to the current recognition request
/// (main actor), which changes once per utterance.
private final class BufferSink: @unchecked Sendable {
    private let lock = NSLock()
    private var _request: SFSpeechAudioBufferRecognitionRequest?

    var request: SFSpeechAudioBufferRecognitionRequest? {
        get { lock.withLock { _request } }
        set { lock.withLock { _request = newValue } }
    }

    func append(_ buffer: AVAudioPCMBuffer) { request?.append(buffer) }
}
