import SwiftUI
import UIKit
import XCTest
@testable import Ganclaw

@MainActor
final class ComposerPasteTests: XCTestCase {
    func testImagePasteOffersPasteWithoutReadingClipboard() {
        let view = ImagePasteTextView()
        view.pasteboard.hasImages = { true }
        view.pasteboard.pastedImage = {
            XCTFail("The edit menu must not read the image")
            return nil
        }
        XCTAssertTrue(view.canPerformAction(#selector(UIResponderStandardEditActions.paste(_:)), withSender: nil))
    }

    func testImagePasteHandsOverTheClipboardImageAndPreservesDraftAndSelection() {
        let view = ImagePasteTextView()
        view.text = "Draft caption"
        view.selectedRange = NSRange(location: 6, length: 7)
        let image = PastedImage(data: Data([1, 2, 3]), mime: "image/png", name: "pasted.png")
        view.pasteboard.hasImages = { true }
        view.pasteboard.pastedImage = { image }
        var pasted: PastedImage?
        view.onImage = { pasted = $0 }

        view.paste(nil)

        XCTAssertEqual(pasted, image)
        XCTAssertEqual(view.text, "Draft caption")
        XCTAssertEqual(view.selectedRange, NSRange(location: 6, length: 7))
    }

    func testUnavailableImageDoesNotStageAttachmentOrChangeDraft() {
        let view = ImagePasteTextView()
        view.text = "Draft"
        view.pasteboard.hasImages = { true }
        view.pasteboard.pastedImage = { nil }
        view.onImage = { _ in XCTFail("No image was available") }

        view.paste(nil)

        XCTAssertEqual(view.text, "Draft")
    }

    // MARK: Focus

    private func composer(focused: Binding<Bool>) -> ComposerTextView {
        ComposerTextView(text: .constant(""), focused: focused, placeholder: "Message", pasteboard: .inert, onImage: { _ in })
    }

    func testFocusChangesFromUIKitAreReportedOnce() {
        var focused = false
        var writes: [Bool] = []
        let binding = Binding(get: { focused }, set: { focused = $0; writes.append($0) })
        let coordinator = composer(focused: binding).makeCoordinator()
        let view = UITextView()

        coordinator.textViewDidBeginEditing(view)
        coordinator.textViewDidBeginEditing(view)
        coordinator.textViewDidEndEditing(view)
        coordinator.textViewDidEndEditing(view)

        XCTAssertEqual(writes, [true, false])
    }

    func testFocusCallbacksDuringSyncDoNotWriteBack() {
        var writes = 0
        let binding = Binding(get: { false }, set: { _ in writes += 1 })
        let coordinator = composer(focused: binding).makeCoordinator()

        coordinator.syncing = true
        coordinator.textViewDidBeginEditing(UITextView())
        coordinator.syncing = false

        XCTAssertEqual(writes, 0)
    }

    func testFocusRequestWithoutAWindowIsKeptForLater() {
        // Becoming first responder needs a window; didMoveToWindow applies
        // the request then. test_app.py covers that path on a simulator.
        let view = ImagePasteTextView()
        view.setWantsFocus(true)
        XCTAssertTrue(view.wantsFocus)
        XCTAssertFalse(view.isFirstResponder)
        view.setWantsFocus(false)
        XCTAssertFalse(view.wantsFocus)
    }
}

final class PastedImageTests: XCTestCase {
    private func image(width: Int, height: Int, alpha: Bool, orientation: UIImage.Orientation = .up) -> UIImage {
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        format.opaque = !alpha
        let base = UIGraphicsImageRenderer(size: CGSize(width: width, height: height), format: format).image { ctx in
            UIColor.red.setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: width, height: height))
        }
        return UIImage(cgImage: base.cgImage!, scale: 1, orientation: orientation)
    }

    private func pixels(_ data: Data) throws -> (Int, Int) {
        let cg = try XCTUnwrap(UIImage(data: data)?.cgImage)
        return (cg.width, cg.height)
    }

    func testClipboardPNGBytesPassThroughUnchanged() throws {
        let png = Data([0x89, 0x50, 0x4E, 0x47, 9, 9, 9])
        let result = try XCTUnwrap(PastedImage.make(png: png, image: image(width: 4, height: 2, alpha: false)))
        XCTAssertEqual(result, PastedImage(data: png, mime: "image/png", name: "pasted.png"))
    }

    func testOpaqueImageIsUprightJPEGAtPixelSize() throws {
        let rotated = image(width: 4, height: 2, alpha: false, orientation: .right)
        let result = try XCTUnwrap(PastedImage.make(png: nil, image: rotated))
        XCTAssertEqual(result.mime, "image/jpeg")
        XCTAssertEqual(result.name, "pasted.jpg")
        let (w, h) = try pixels(result.data)
        XCTAssertEqual(w, 2, "orientation is drawn into the pixels")
        XCTAssertEqual(h, 4)
    }

    func testTransparentImageStaysPNG() throws {
        let result = try XCTUnwrap(PastedImage.make(png: nil, image: image(width: 3, height: 5, alpha: true)))
        XCTAssertEqual(result.mime, "image/png")
        let (w, h) = try pixels(result.data)
        XCTAssertEqual(w, 3)
        XCTAssertEqual(h, 5)
    }

    func testRetinaImageKeepsItsPixelsRegardlessOfDeviceScale() throws {
        let format = UIGraphicsImageRendererFormat()
        format.scale = 3
        format.opaque = true
        let retina = UIGraphicsImageRenderer(size: CGSize(width: 2, height: 2), format: format).image { ctx in
            UIColor.blue.setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: 2, height: 2))
        }
        let result = try XCTUnwrap(PastedImage.make(png: nil, image: retina))
        let (w, h) = try pixels(result.data)
        XCTAssertEqual(w, 6)
        XCTAssertEqual(h, 6)
    }

    func testSameInputGivesSameBytes() {
        let source = image(width: 8, height: 6, alpha: false, orientation: .left)
        XCTAssertEqual(PastedImage.make(png: nil, image: source), PastedImage.make(png: nil, image: source))
    }

    func testNoImageGivesNothing() {
        XCTAssertNil(PastedImage.make(png: nil, image: nil))
        XCTAssertNil(PastedImage.make(png: Data(), image: nil))
    }
}
