import Foundation
import XCTest
@testable import Ganclaw

final class ReplySwipeTests: XCTestCase {
    func testLeftSwipeSelectsReplyAndCapsTheRowMovement() {
        XCTAssertTrue(ReplySwipe.selectsReply(CGSize(width: -70, height: 5)))
        XCTAssertEqual(ReplySwipe.offset(CGSize(width: -300, height: 0)), -88)
    }

    func testScrollingAndShortOrRightwardDragsDoNotSelectReply() {
        XCTAssertFalse(ReplySwipe.selectsReply(CGSize(width: -25, height: 0)))
        XCTAssertFalse(ReplySwipe.selectsReply(CGSize(width: 100, height: 0)))
        XCTAssertFalse(ReplySwipe.selectsReply(CGSize(width: -70, height: 120)))
        XCTAssertEqual(ReplySwipe.offset(CGSize(width: -70, height: 120)), 0)
        XCTAssertEqual(ReplySwipe.offset(.zero), 0)
    }
}
