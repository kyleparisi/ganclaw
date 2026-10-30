import UIKit
import XCTest
@testable import Ganclaw

@MainActor
final class ComposerPasteTests: XCTestCase {
    func testImagePasteOffersPasteWithoutReadingClipboard() {
        let view = ImagePasteTextView()
        view.pasteboard.hasImages = { true }
        view.pasteboard.image = {
            XCTFail("The edit menu must not read the image")
            return nil
        }
        XCTAssertTrue(view.canPerformAction(#selector(UIResponderStandardEditActions.paste(_:)), withSender: nil))
    }

    func testImagePasteProducesPNGAndPreservesDraftAndSelection() throws {
        let view = ImagePasteTextView()
        view.text = "Draft caption"
        view.selectedRange = NSRange(location: 6, length: 7)
        let image = UIGraphicsImageRenderer(size: CGSize(width: 2, height: 2)).image { context in
            UIColor.red.setFill()
            context.fill(CGRect(x: 0, y: 0, width: 2, height: 2))
        }
        view.pasteboard.hasImages = { true }
        view.pasteboard.image = { image }
        var pasted: Data?
        view.onImage = { pasted = $0 }

        view.paste(nil)

        let decoded = try XCTUnwrap(UIImage(data: XCTUnwrap(pasted)))
        XCTAssertEqual(decoded.cgImage?.width, image.cgImage?.width)
        XCTAssertEqual(decoded.cgImage?.height, image.cgImage?.height)
        XCTAssertEqual(view.text, "Draft caption")
        XCTAssertEqual(view.selectedRange, NSRange(location: 6, length: 7))
    }

    func testUnavailableImageDoesNotStageAttachmentOrChangeDraft() {
        let view = ImagePasteTextView()
        view.text = "Draft"
        view.pasteboard.hasImages = { true }
        view.pasteboard.image = { nil }
        view.onImage = { _ in XCTFail("No image was available") }

        view.paste(nil)

        XCTAssertEqual(view.text, "Draft")
    }
}
