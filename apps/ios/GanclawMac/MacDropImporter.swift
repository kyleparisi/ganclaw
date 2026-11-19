import AppKit
import SwiftUI
import UniformTypeIdentifiers

/// One AppKit drop destination around a SwiftUI view. AppKit supplies one
/// pasteboard object per item, choosing its first supported representation.
struct MacDropTarget<Content: View>: NSViewRepresentable {
    var content: Content
    var targeted: (Bool) -> Void
    var perform: ([NSPasteboardReading]) -> Void

    func makeNSView(context: Context) -> DropView<Content> {
        let view = DropView(rootView: content)
        view.targeted = targeted
        view.perform = perform
        return view
    }

    func updateNSView(_ view: DropView<Content>, context: Context) {
        view.host.rootView = content
        view.invalidateIntrinsicContentSize()
        view.targeted = targeted
        view.perform = perform
    }

    func sizeThatFits(_ proposal: ProposedViewSize, nsView: DropView<Content>, context: Context) -> CGSize? {
        let fitting = nsView.host.fittingSize
        return CGSize(width: proposal.width ?? fitting.width, height: proposal.height ?? fitting.height)
    }

    final class DropView<Root: View>: NSView {
        let host: NSHostingView<Root>
        var targeted: (Bool) -> Void = { _ in }
        var perform: ([NSPasteboardReading]) -> Void = { _ in }

        init(rootView: Root) {
            host = NSHostingView(rootView: rootView)
            super.init(frame: .zero)
            addSubview(host)
            host.autoresizingMask = [.width, .height]
            let types = NSFilePromiseReceiver.readableDraggedTypes.map { NSPasteboard.PasteboardType($0) }
            registerForDraggedTypes(types + [.fileURL, .png, .tiff])
        }
        required init?(coder: NSCoder) { fatalError("init(coder:) is not supported") }
        override func layout() { super.layout(); host.frame = bounds }
        override func draggingEntered(_ sender: NSDraggingInfo) -> NSDragOperation {
            targeted(true)
            return .copy
        }
        override func draggingExited(_ sender: NSDraggingInfo?) { targeted(false) }
        override func performDragOperation(_ sender: NSDraggingInfo) -> Bool {
            targeted(false)
            let objects = MacDropImporter.items(from: sender.draggingPasteboard)
            guard !objects.isEmpty else { return false }
            perform(objects)
            return true
        }
    }
}

enum MacDropImporter {
    struct Attachment {
        let name: String
        let mime: String
        let data: Data
    }

    struct DropOutcome {
        var files: [Attachment] = []
        var failed = false
        var finished = true
    }

    static func items(from pasteboard: NSPasteboard) -> [NSPasteboardReading] {
        pasteboard.readObjects(forClasses: [NSFilePromiseReceiver.self, NSURL.self, NSImage.self],
                               options: [.urlReadingFileURLsOnly: true]) as? [NSPasteboardReading] ?? []
    }

    /// Completion is delivered once per item, including a timeout if a source
    /// stops responding. Late callbacks are ignored after the timeout.
    static func load(_ item: NSPasteboardReading, timeout: TimeInterval = 300,
                     completion: @escaping (DropOutcome) -> Void) {
        let gate = CompletionGate(completion)
        if let promise = item as? NSFilePromiseReceiver {
            let lease = PromiseLease(promise)
            let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
            do { try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true) }
            catch { lease.release(); gate.finish(DropOutcome(failed: true)); return }
            let queue = OperationQueue()
            queue.maxConcurrentOperationCount = 1
            // Keep the destination available for any additional callbacks.
            // The system's temporary-directory cleanup handles app termination.
            DispatchQueue.global().asyncAfter(deadline: .now() + 3600) {
                try? FileManager.default.removeItem(at: directory)
            }
            let batch = PromiseBatch { result in
                if result.finished { gate.finish(result) } else { completion(result) }
            }
            DispatchQueue.global().asyncAfter(deadline: .now() + timeout) {
                batch.timeout()
                lease.release()
            }
            promise.receivePromisedFiles(atDestination: directory, options: [:], operationQueue: queue) { url, error in
                do {
                    if let error { throw error }
                    defer { try? FileManager.default.removeItem(at: url) }
                    // fileNames is populated only after receivePromisedFiles is called.
                    batch.receive(.success(try read(url)))
                } catch { batch.receive(.failure(error)) }
            }
            batch.setExpectedCount(max(1, promise.fileNames.count))
        } else if let url = item as? URL, url.isFileURL {
            DispatchQueue.global().async {
                let scoped = url.startAccessingSecurityScopedResource()
                defer { if scoped { url.stopAccessingSecurityScopedResource() } }
                do { gate.finish(DropOutcome(files: [try read(url)])) }
                catch { gate.finish(DropOutcome(failed: true)) }
            }
        } else if let image = item as? NSImage, let data = PastedImage.make(png: nil, image: image) {
            gate.finish(DropOutcome(files: [Attachment(name: data.name, mime: data.mime, data: data.data)]))
        } else {
            gate.finish(DropOutcome(failed: true))
        }
    }

    private static func read(_ url: URL) throws -> Attachment {
        let values = try url.resourceValues(forKeys: [.isDirectoryKey])
        guard values.isDirectory != true else { throw CocoaError(.fileReadUnsupportedScheme) }
        let mime = UTType(filenameExtension: url.pathExtension)?.preferredMIMEType ?? "application/octet-stream"
        return Attachment(name: url.lastPathComponent, mime: mime, data: try Data(contentsOf: url))
    }

    /// Collect all files from one promise before enabling Send. Legacy drag
    /// sources can deliver more than one file through a single receiver.
    final class PromiseBatch {
        private let lock = NSLock()
        private var outcome = DropOutcome()
        private var count = 0
        private var expectedCount: Int?
        private let completion: (DropOutcome) -> Void
        private var completed = false
        init(_ completion: @escaping (DropOutcome) -> Void) { self.completion = completion }

        func setExpectedCount(_ expected: Int) {
            lock.lock()
            expectedCount = expected
            finishIfReady()
        }

        func receive(_ result: Result<Attachment, Error>) {
            lock.lock()
            if completed {
                var late = DropOutcome(finished: false)
                switch result {
                case .failure: late.failed = true
                case .success(let file): late.files.append(file)
                }
                lock.unlock()
                completion(late)
                return
            }
            count += 1
            switch result {
            case .failure: outcome.failed = true
            case .success(let file): outcome.files.append(file)
            }
            finishIfReady()
        }

        // Called with the lock held; releases it before invoking the callback.
        private func finishIfReady() {
            guard !completed, let expectedCount, count >= expectedCount else { lock.unlock(); return }
            let received = outcome
            completed = true
            lock.unlock()
            completion(received)
        }

        func timeout() {
            lock.lock()
            guard !completed else { lock.unlock(); return }
            outcome.failed = true
            let received = outcome
            completed = true
            lock.unlock()
            completion(received)
        }
    }

    private final class PromiseLease {
        private let lock = NSLock()
        private var promise: NSFilePromiseReceiver?
        init(_ promise: NSFilePromiseReceiver) { self.promise = promise }
        func release() {
            lock.lock()
            promise = nil
            lock.unlock()
        }
    }

    private final class CompletionGate {
        private let lock = NSLock()
        private var completion: ((DropOutcome) -> Void)?
        init(_ completion: @escaping (DropOutcome) -> Void) { self.completion = completion }
        func finish(_ result: DropOutcome) {
            lock.lock()
            let callback = completion
            completion = nil
            lock.unlock()
            callback?(result)
        }
    }
}
