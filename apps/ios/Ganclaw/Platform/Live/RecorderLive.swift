import AVFoundation
import Foundation

extension RecorderClient {
    static func live(check: CapabilityCheck) -> RecorderClient {
        let r = LiveRecorder(check: check)
        return RecorderClient(
            requestAccess: { try await r.requestAccess() },
            start: { try r.start() },
            stop: { r.stop() },
            cancel: { r.cancel() },
            elapsed: { r.elapsed }
        )
    }
}

/// Records voice notes as AAC (.m4a), which ganclaw can transcribe.
@MainActor
private final class LiveRecorder {
    private let check: CapabilityCheck
    private var recorder: AVAudioRecorder?
    private let url = FileManager.default.temporaryDirectory.appendingPathComponent("voice-note.m4a")

    init(check: CapabilityCheck) { self.check = check }

    var elapsed: TimeInterval { recorder?.currentTime ?? 0 }

    func requestAccess() async throws {
        try check.require(.microphone)
        guard await AVAudioApplication.requestRecordPermission() else { throw PlatformError.denied(.microphone) }
    }

    func start() throws {
        try check.require(.microphone)
        let s = AVAudioSession.sharedInstance()
        try s.setCategory(.playAndRecord, mode: .default, options: [.defaultToSpeaker])
        try s.setActive(true)
        let r = try AVAudioRecorder(url: url, settings: [
            AVFormatIDKey: Int(kAudioFormatMPEG4AAC),
            AVSampleRateKey: 16000,
            AVNumberOfChannelsKey: 1,
            AVEncoderAudioQualityKey: AVAudioQuality.medium.rawValue,
        ])
        guard r.record() else { throw PlatformError.unavailable("Couldn't start recording.") }
        recorder = r
    }

    func stop() -> Data? {
        let long = elapsed >= 0.5
        finish()
        return long ? try? Data(contentsOf: url) : nil
    }

    func cancel() { finish() }

    private func finish() {
        recorder?.stop()
        recorder = nil
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }
}
