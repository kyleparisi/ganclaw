import AppKit
import UniformTypeIdentifiers
import XCTest

final class MacDropImporterTests: XCTestCase {
    func testMixedPasteboardReadsEachItemOnce() throws {
        let board = NSPasteboard(name: .init(UUID().uuidString))
        defer { board.releaseGlobally() }
        let writer = PromiseWriter()
        let promise = NSFilePromiseProvider(fileType: UTType.png.identifier, delegate: writer)
        let file = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".txt")
        try Data("hello".utf8).write(to: file)
        defer { try? FileManager.default.removeItem(at: file) }
        XCTAssertTrue(board.writeObjects([promise, file as NSURL]))
        let items = MacDropImporter.items(from: board)
        XCTAssertEqual(items.count, 2)
        XCTAssertEqual(items.filter { $0 is NSFilePromiseReceiver }.count, 1)
        XCTAssertEqual(items.filter { $0 is NSURL }.count, 1)
    }

    func testPromiseBatchWaitsForEveryFile() throws {
        var result: MacDropImporter.DropOutcome?
        let batch = MacDropImporter.PromiseBatch { result = $0 }
        let first = MacDropImporter.Attachment(name: "one.png", mime: "image/png", data: Data([1]))
        let second = MacDropImporter.Attachment(name: "two.png", mime: "image/png", data: Data([2]))
        batch.setExpectedCount(2)
        batch.receive(.success(first))
        XCTAssertNil(result)
        batch.receive(.success(second))
        let files = try XCTUnwrap(result).files
        XCTAssertEqual(files.map(\.name), ["one.png", "two.png"])
        XCTAssertEqual(files.map(\.data), [Data([1]), Data([2])])
    }

    func testPromiseBatchPreservesFilesWhenAnotherFileFails() throws {
        var result: MacDropImporter.DropOutcome?
        let batch = MacDropImporter.PromiseBatch { result = $0 }
        let file = MacDropImporter.Attachment(name: "one.png", mime: "image/png", data: Data([1]))
        batch.setExpectedCount(2)
        batch.receive(.success(file))
        batch.receive(.failure(CocoaError(.fileReadUnknown)))
        let outcome = try XCTUnwrap(result)
        XCTAssertTrue(outcome.failed)
        XCTAssertEqual(outcome.files.map(\.name), ["one.png"])
    }

    func testPromiseBatchTimeoutPreservesReceivedFiles() throws {
        var result: MacDropImporter.DropOutcome?
        let batch = MacDropImporter.PromiseBatch { result = $0 }
        let file = MacDropImporter.Attachment(name: "one.png", mime: "image/png", data: Data([1]))
        batch.receive(.success(file))
        XCTAssertNil(result)
        batch.timeout()
        let outcome = try XCTUnwrap(result)
        XCTAssertTrue(outcome.failed)
        XCTAssertEqual(outcome.files.map(\.name), ["one.png"])
    }

    func testPromiseBatchHandlesCallbackBeforeExpectedCount() throws {
        var result: MacDropImporter.DropOutcome?
        let batch = MacDropImporter.PromiseBatch { result = $0 }
        let file = MacDropImporter.Attachment(name: "one.png", mime: "image/png", data: Data([1]))
        batch.receive(.success(file))
        XCTAssertNil(result)
        batch.setExpectedCount(1)
        XCTAssertEqual(try XCTUnwrap(result).files.map(\.name), ["one.png"])
    }

    func testUnexpectedLaterPromiseFileIsNotDiscarded() {
        var results: [MacDropImporter.DropOutcome] = []
        let batch = MacDropImporter.PromiseBatch { results.append($0) }
        let first = MacDropImporter.Attachment(name: "one.png", mime: "image/png", data: Data([1]))
        let second = MacDropImporter.Attachment(name: "two.png", mime: "image/png", data: Data([2]))
        batch.setExpectedCount(1)
        batch.receive(.success(first))
        batch.receive(.success(second))
        XCTAssertEqual(results.count, 2)
        XCTAssertTrue(results[0].finished)
        XCTAssertFalse(results[1].finished)
        XCTAssertEqual(results[1].files.map(\.name), ["two.png"])
    }

    func testPromiseTimeoutCompletesOnce() throws {
        let board = NSPasteboard(name: .init(UUID().uuidString))
        defer { board.releaseGlobally() }
        let writer = PromiseWriter()
        let promise = NSFilePromiseProvider(fileType: UTType.png.identifier, delegate: writer)
        XCTAssertTrue(board.writeObjects([promise]))
        let receiver = try XCTUnwrap(MacDropImporter.items(from: board).first as? NSFilePromiseReceiver)
        let received = expectation(description: "Promise completes or times out")
        received.assertForOverFulfill = true
        MacDropImporter.load(receiver, timeout: 0.2) { _ in received.fulfill() }
        wait(for: [received], timeout: 3)
    }

    func testLoadsFileURL() throws {
        let file = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".txt")
        let contents = Data("hello".utf8)
        try contents.write(to: file)
        defer { try? FileManager.default.removeItem(at: file) }
        let received = expectation(description: "File loaded")
        MacDropImporter.load(file as NSURL) { result in
            do {
                let attachment = try XCTUnwrap(result.files.first)
                XCTAssertEqual(attachment.data, contents)
                XCTAssertEqual(attachment.name, file.lastPathComponent)
                XCTAssertEqual(attachment.mime, "text/plain")
            } catch { XCTFail("Could not load file: \(error)") }
            received.fulfill()
        }
        wait(for: [received], timeout: 5)
    }

    func testUnreadableFileReportsFailure() {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        let received = expectation(description: "Failure reported")
        MacDropImporter.load(url as NSURL) { result in
            XCTAssertTrue(result.failed)
            received.fulfill()
        }
        wait(for: [received], timeout: 5)
    }

    func testLoadsImageData() throws {
        let image = NSImage(size: NSSize(width: 2, height: 2))
        image.lockFocus()
        NSColor.red.setFill()
        NSRect(x: 0, y: 0, width: 2, height: 2).fill()
        image.unlockFocus()
        let received = expectation(description: "Image loaded")
        MacDropImporter.load(image) { result in
            do {
                let attachment = try XCTUnwrap(result.files.first)
                XCTAssertTrue(attachment.mime.hasPrefix("image/"))
                XCTAssertFalse(attachment.data.isEmpty)
            } catch { XCTFail("Could not load image: \(error)") }
            received.fulfill()
        }
        wait(for: [received], timeout: 5)
    }
}

private final class PromiseWriter: NSObject, NSFilePromiseProviderDelegate {
    func filePromiseProvider(_ filePromiseProvider: NSFilePromiseProvider, fileNameForType fileType: String) -> String { "screenshot.png" }
    func filePromiseProvider(_ filePromiseProvider: NSFilePromiseProvider, writePromiseTo url: URL,
                             completionHandler: @escaping (Error?) -> Void) {
        do { try Data([1, 2, 3]).write(to: url); completionHandler(nil) }
        catch { completionHandler(error) }
    }
}
