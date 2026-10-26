import AVFoundation
import Observation
import QuickLook
import SwiftUI

/// Images inline, everything else as a file card; tapping opens QuickLook.
/// Downloads carry the device token, so AsyncImage can't be used.
struct FileView: View {
    var file: AppFile
    var onUser: Bool
    @Environment(AppStore.self) private var store
    @State private var image: UIImage?
    @State private var preview: URL?
    @State private var loading = false

    var body: some View {
        CButton("file.\(file.id)") { Task { await open() } } label: { content }
            .buttonStyle(.plain)
            .accessibilityLabel("\(file.name), \(file.size.fileSize)")
            .task(id: file.id) { if file.isImage { await loadImage() } }
            .quickLookPreview($preview)
    }

    @ViewBuilder
    private var content: some View {
        if file.isImage {
            ZStack(alignment: .bottomTrailing) {
                if let image {
                    Image(uiImage: image).resizable().scaledToFill()
                } else {
                    Theme.segment
                    ProgressView().frame(maxWidth: .infinity, maxHeight: .infinity)
                }
                Text(file.name)
                    .font(.system(size: 11)).foregroundStyle(.white)
                    .padding(.horizontal, 6).padding(.vertical, 2)
                    .background(.black.opacity(0.55), in: RoundedRectangle(cornerRadius: 6))
                    .padding(8)
            }
            .frame(width: 220, height: 150)
            .clipShape(RoundedRectangle(cornerRadius: 16, style: .continuous))
        } else {
            HStack(spacing: 10) {
                Text(file.badge)
                    .font(.system(size: 10, weight: .semibold, design: .monospaced))
                    .foregroundStyle(file.mime == "application/pdf" ? Theme.red : Theme.text2)
                    .frame(width: 34, height: 34)
                    .background(file.mime == "application/pdf" ? Theme.redSoft : Theme.chip, in: RoundedRectangle(cornerRadius: 8))
                VStack(alignment: .leading, spacing: 1) {
                    Text(file.name).font(.system(size: 13, weight: .semibold, design: file.name.contains(".") ? .monospaced : .default)).lineLimit(1)
                    Text(file.size.fileSize).font(.system(size: 12)).foregroundStyle(Theme.muted)
                }
                Spacer(minLength: 0)
                if loading { ProgressView() }
            }
            .padding(.horizontal, 12).padding(.vertical, 10)
            .frame(width: 230)
            .background(Theme.card, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 14, style: .continuous).stroke(Theme.line))
            .foregroundStyle(Theme.ink)
        }
    }

    private func loadImage() async {
        if let cached = FileCache.shared.image(file.id) { image = cached; return }
        guard let api = store.api, let data = try? await api.download(file), let img = UIImage(data: data) else { return }
        FileCache.shared.store(img, file.id)
        image = img
    }

    private func open() async {
        guard let api = store.api, !loading else { return }
        loading = true
        defer { loading = false }
        do {
            let url = FileManager.default.temporaryDirectory.appendingPathComponent(file.id, isDirectory: true)
            try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
            let dest = url.appendingPathComponent(file.name)
            if !FileManager.default.fileExists(atPath: dest.path) {
                try await api.download(file).write(to: dest)
            }
            preview = dest
        } catch {
            store.send(.banner(store.describe(error)))
        }
    }
}

final class FileCache {
    static let shared = FileCache()
    private let cache = NSCache<NSString, UIImage>()
    func image(_ id: String) -> UIImage? { cache.object(forKey: id as NSString) }
    func store(_ img: UIImage, _ id: String) { cache.setObject(img, forKey: id as NSString) }
}

/// Records a voice note as AAC (.m4a), which ganclaw can transcribe.
@MainActor
@Observable
final class VoiceRecorder {
    private(set) var recording = false
    private(set) var elapsed: TimeInterval = 0
    @ObservationIgnored private var recorder: AVAudioRecorder?
    @ObservationIgnored private var timer: Timer?
    private let url = FileManager.default.temporaryDirectory.appendingPathComponent("voice-note.m4a")

    var elapsedText: String { String(format: "%d:%02d", Int(elapsed) / 60, Int(elapsed) % 60) }

    func start() async -> Bool {
        guard await AVAudioApplication.requestRecordPermission() else { return false }
        do {
            let s = AVAudioSession.sharedInstance()
            try s.setCategory(.playAndRecord, mode: .default, options: [.defaultToSpeaker, .allowBluetooth])
            try s.setActive(true)
            let r = try AVAudioRecorder(url: url, settings: [
                AVFormatIDKey: Int(kAudioFormatMPEG4AAC),
                AVSampleRateKey: 16000,
                AVNumberOfChannelsKey: 1,
                AVEncoderAudioQualityKey: AVAudioQuality.medium.rawValue,
            ])
            guard r.record() else { return false }
            recorder = r
            recording = true
            elapsed = 0
            timer = Timer.scheduledTimer(withTimeInterval: 0.5, repeats: true) { [weak self] _ in
                Task { @MainActor in self?.elapsed = self?.recorder?.currentTime ?? 0 }
            }
            return true
        } catch {
            return false
        }
    }

    /// Stops and returns the recording, or nil if it was too short.
    func stop() -> Data? {
        let long = (recorder?.currentTime ?? 0) >= 0.5
        finish()
        return long ? try? Data(contentsOf: url) : nil
    }

    func cancel() { finish() }

    private func finish() {
        recorder?.stop()
        recorder = nil
        timer?.invalidate()
        timer = nil
        recording = false
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
    }
}
