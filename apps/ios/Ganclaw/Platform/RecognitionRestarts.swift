import Foundation

/// When to start the next recognition task after one ends, and whether to
/// keep requiring on-device recognition. Pure, so it is unit-tested.
///
/// A task that ran for a while and then ended (an utterance finished, or
/// "no speech detected" after a stretch of silence) is normal: restart at
/// once. A task that fails right after starting means something is wrong
/// (recognizer busy, on-device model missing); restarting at once would
/// spin, so back off and, after a few, stop requiring on-device recognition.
struct RecognitionRestarts {
    /// A task that fails sooner than this counts as a quick failure.
    var quickFailure: TimeInterval = 1.0
    var maxDelay: TimeInterval = 5
    /// Quick failures in a row before giving up on on-device recognition.
    var onDeviceLimit = 3

    private(set) var quickFailures = 0
    private(set) var onDeviceAllowed = true

    /// A task produced speech: whatever happened before is resolved.
    mutating func heard() { quickFailures = 0 }

    /// A task ended after running `lifetime` seconds. Returns how long to
    /// wait before starting the next one.
    mutating func ended(after lifetime: TimeInterval, failed: Bool) -> TimeInterval {
        guard failed, lifetime < quickFailure else {
            quickFailures = 0
            return 0
        }
        quickFailures += 1
        if quickFailures >= onDeviceLimit { onDeviceAllowed = false }
        return min(maxDelay, 0.25 * pow(2, Double(quickFailures - 1)))
    }
}
