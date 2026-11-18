import AppKit
import UniformTypeIdentifiers
import XCTest

@MainActor
final class ComposerPasteTests: XCTestCase {
    func testTabCompletesAnAvailableCommand() throws {
        let view = ComposerNSTextView()
        var completed = false
        view.onComplete = { completed = true; return true }
        let event = try XCTUnwrap(NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [], timestamp: 0,
                                                   windowNumber: 0, context: nil, characters: "\t",
                                                   charactersIgnoringModifiers: "\t", isARepeat: false, keyCode: 48))
        view.keyDown(with: event)
        XCTAssertTrue(completed)
    }

    func testImageEnablesPasteAndPreservesDraft() {
        let view = ComposerNSTextView()
        let image = PastedImage(data: Data([1, 2, 3]), mime: "image/png", name: "pasted.png")
        view.string = "Draft caption"
        view.setSelectedRange(NSRange(location: 6, length: 7))
        view.pasteboard.hasImages = { true }
        view.pasteboard.pastedImage = { image }
        var received: PastedImage?
        view.onImage = { received = $0 }
        let item = NSMenuItem(title: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "")

        XCTAssertTrue(view.validateUserInterfaceItem(item))
        view.paste(nil)
        XCTAssertEqual(received, image)
        XCTAssertEqual(view.string, "Draft caption")
        XCTAssertEqual(view.selectedRange(), NSRange(location: 6, length: 7))
    }

    func testUnreadableImageDoesNotChangeDraft() {
        let view = ComposerNSTextView()
        view.string = "Draft"
        view.pasteboard.hasImages = { true }
        view.pasteboard.pastedImage = { nil }
        view.onImage = { _ in XCTFail("No image was available") }
        var reported = false
        view.onPasteError = { reported = true }
        view.paste(nil)
        XCTAssertTrue(reported)
        XCTAssertEqual(view.string, "Draft")
    }

    func testPasteboardAcceptsPNGAndJPEGImageData() throws {
        let board = NSPasteboard(name: .init(UUID().uuidString))
        defer { board.releaseGlobally() }
        let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 2, pixelsHigh: 2,
                                      bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true,
                                      isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
        let png = try XCTUnwrap(bitmap.representation(using: .png, properties: [:]))
        board.declareTypes([.png], owner: nil)
        board.setData(png, forType: .png)
        let client = PasteboardClient.macPasteboard(board)
        XCTAssertTrue(client.hasImages())
        XCTAssertEqual(client.pastedImage()?.data, png)
        XCTAssertEqual(client.pastedImage()?.mime, "image/png")

        let jpeg = try XCTUnwrap(bitmap.representation(using: .jpeg, properties: [:]))
        board.declareTypes([NSPasteboard.PasteboardType(UTType.jpeg.identifier)], owner: nil)
        board.setData(jpeg, forType: NSPasteboard.PasteboardType(UTType.jpeg.identifier))
        XCTAssertTrue(client.hasImages())
        XCTAssertNotNil(client.pastedImage())
        XCTAssertTrue(["image/png", "image/jpeg"].contains(client.pastedImage()?.mime ?? ""))
    }

    func testPasteValidationDoesNotReadClipboardContents() {
        let view = ComposerNSTextView()
        view.pasteboard.hasFiles = { true }
        view.pasteboard.fileURLs = { XCTFail("Validation must not read files"); return [] }
        view.pasteboard.pastedImage = { XCTFail("Validation must not read images"); return nil }
        let item = NSMenuItem(title: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "")
        XCTAssertTrue(view.validateUserInterfaceItem(item))
    }

    func testPasteForwardsEveryCopiedFile() {
        let view = ComposerNSTextView()
        let files = [URL(fileURLWithPath: "/tmp/one.png"), URL(fileURLWithPath: "/tmp/two.pdf")]
        view.pasteboard.hasFiles = { true }
        view.pasteboard.hasImages = { true }
        view.pasteboard.pastedImage = { XCTFail("File URLs take precedence"); return nil }
        view.pasteboard.fileURLs = { files }
        var received: [URL] = []
        view.onFiles = { received = $0 }
        view.paste(nil)
        XCTAssertEqual(received, files)
    }

    func testPasteboardExposesCopiedFilesWithoutTreatingThemAsImages() {
        let board = NSPasteboard(name: .init(UUID().uuidString))
        defer { board.releaseGlobally() }
        let files = [URL(fileURLWithPath: "/tmp/one.png"), URL(fileURLWithPath: "/tmp/two.pdf")]
        board.clearContents()
        board.writeObjects(files.map { $0 as NSURL })
        let client = PasteboardClient.macPasteboard(board)
        XCTAssertTrue(client.hasFiles())
        XCTAssertFalse(client.hasImages())
        XCTAssertEqual(client.fileURLs(), files)
    }

}
