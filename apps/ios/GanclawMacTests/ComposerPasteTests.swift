import AppKit
import UniformTypeIdentifiers
import XCTest

@MainActor
final class ComposerPasteTests: XCTestCase {
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
        view.paste(nil)
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
    }

    func testFinderImageFilePreservesNameAndBytes() throws {
        let board = NSPasteboard(name: .init(UUID().uuidString))
        defer { board.releaseGlobally() }
        let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 2, pixelsHigh: 2,
                                      bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true,
                                      isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)!
        let bytes = try XCTUnwrap(bitmap.representation(using: .png, properties: [:]))
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let file = directory.appendingPathComponent("image.png")
        try bytes.write(to: file)
        board.writeObjects([file as NSURL])
        let client = PasteboardClient.macPasteboard(board)
        XCTAssertTrue(client.hasImages())
        XCTAssertEqual(client.pastedImage(), PastedImage(data: bytes, mime: "image/png", name: "image.png"))
    }
}
