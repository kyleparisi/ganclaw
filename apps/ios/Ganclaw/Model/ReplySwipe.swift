import Foundation

/// A deliberate leftward swipe; scrolling or a short drag must not select a reply.
enum ReplySwipe {
    static func offset(_ translation: CGSize) -> CGFloat {
        guard translation.width < 0, abs(translation.width) > abs(translation.height) * 1.5 else { return 0 }
        return max(-88, translation.width)
    }

    static func selectsReply(_ translation: CGSize) -> Bool {
        offset(translation) <= -64
    }
}
