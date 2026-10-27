import XCTest
@testable import Ganclaw

/// The capability manifest and its runtime check.
final class CapabilityTests: XCTestCase {
    private func check(info: [String: String], entitlements: Set<String>? = nil) -> CapabilityCheck {
        CapabilityCheck(
            infoValue: { info[$0] },
            entitlement: entitlements.map { set in { set.contains($0) } }
        )
    }

    private var allUsage: [String: String] {
        Dictionary(uniqueKeysWithValues: Capability.allCases.map { ($0.usageKey, $0.usageDescription) })
    }

    func testBuiltAppDeclaresEveryCapability() {
        // Runs inside the app: Info.plist is the one gencap generated.
        for c in Capability.allCases {
            XCTAssertNil(CapabilityCheck.live.missing(c), "\(c) is not declared in the built app")
        }
    }

    func testMissingUsageStringIsAnError() {
        var info = allUsage
        info["NSMicrophoneUsageDescription"] = nil
        let c = check(info: info)
        XCTAssertEqual(c.missing(.microphone), .undeclared(.microphone, missing: "NSMicrophoneUsageDescription in Info.plist"))
        XCTAssertEqual(c.missing(.speechRecognition), .undeclared(.microphone, missing: "NSMicrophoneUsageDescription in Info.plist"),
                       "speech recognition needs the microphone's declarations too")
        XCTAssertNil(c.missing(.camera))
    }

    func testMissingMacEntitlementIsAnError() {
        let c = check(info: allUsage, entitlements: ["com.apple.security.device.camera"])
        XCTAssertEqual(c.missing(.microphone), .undeclared(.microphone, missing: "the com.apple.security.device.audio-input entitlement"))
        XCTAssertNotNil(c.missing(.speechRecognition))
        XCTAssertNil(c.missing(.camera))
        XCTAssertNil(c.missing(.photoLibrary), "the photo picker needs no entitlement")
    }

    func testEntitlementsAreNotCheckedWhereTheOSDoesNotEnforceThem() {
        XCTAssertNil(check(info: allUsage, entitlements: nil).missing(.microphone))
    }

    func testNotificationsUseSystemAuthorizationWithoutUsageText() {
        XCTAssertNil(check(info: [:]).missing(.notifications))
        XCTAssertEqual(Capability.iosEntitlements, ["aps-environment": "$(APNS_ENVIRONMENT)"])
    }

    func testManifestEntitlements() {
        XCTAssertEqual(Capability.macEntitlements, ["com.apple.security.device.audio-input", "com.apple.security.device.camera"])
    }
}

/// The store against stub platform clients, the way investor-be's handler
/// tests build State with stub SNS/SendGrid clients.
@MainActor
final class PlatformClientTests: XCTestCase {
    func testCopyUsesThePasteboardClient() {
        var copied: String?
        var p = Platform.inert
        p.pasteboard.setString = { copied = $0 }
        let store = AppStore(platform: p)

        store.copy("Moving our call to Thursday", block: "1.1", label: "Subject")

        XCTAssertEqual(copied, "Moving our call to Thursday")
        XCTAssertEqual(store.state.toast, "Subject copied")
    }

    func testDriveModeReportsADeniedPermission() async {
        var p = Platform.inert
        p.speech.requestAccess = { throw PlatformError.denied(.speechRecognition) }
        var listened = false
        p.speech.listen = { _ in listened = true }
        let store = AppStore(platform: p)
        store.send(.agentsLoaded([AgentSummary(name: "infra", state: .idle, lastMessage: nil, needsYou: false)]))

        await store.startDrive("infra")

        XCTAssertFalse(listened)
        XCTAssertEqual(store.state.drive.error, "Allow speech recognition access for Ganclaw in Settings.")
    }

    func testDriveModeReportsAnUndeclaredCapabilityInsteadOfCrashing() async {
        var p = Platform.inert
        p.speech = .live(check: CapabilityCheck(infoValue: { _ in nil }, entitlement: nil))
        let store = AppStore(platform: p)
        store.send(.agentsLoaded([AgentSummary(name: "infra", state: .idle, lastMessage: nil, needsYou: false)]))

        await store.startDrive("infra")

        XCTAssertEqual(store.state.drive.error, PlatformError.undeclared(.microphone, missing: "NSMicrophoneUsageDescription in Info.plist").errorDescription)
    }

    func testDriveModeListensAndSpeaksThroughTheClient() async {
        let fake = FakeSpeech()
        var p = Platform.inert
        p.speech = fake.client
        var idleDisabled: [Bool] = []
        p.device.setIdleTimerDisabled = { idleDisabled.append($0) }
        let store = AppStore(platform: p)
        store.send(.agentsLoaded([AgentSummary(name: "infra", state: .idle, lastMessage: nil, needsYou: false)]))

        await store.startDrive("infra")
        XCTAssertTrue(fake.listening)
        fake.hear("did the fix", final: false)
        XCTAssertEqual(store.state.drive.heard, "did the fix")

        store.exitDrive()
        XCTAssertFalse(fake.listening)
        XCTAssertEqual(idleDisabled, [true, false])
    }

    func testNotificationsAreNotRequestedBeforePairing() async {
        var p = Platform.inert
        var requested = false
        p.notifications.enable = { requested = true }
        let store = AppStore(platform: p)
        await store.enableNotifications()
        XCTAssertFalse(requested)
    }

    func testUnpairClearsTheKeychain() {
        var cleared = false
        var p = Platform.inert
        p.keychain.clear = { cleared = true }
        let store = AppStore(platform: p)

        store.unpair()

        XCTAssertTrue(cleared)
    }
}

/// Drive mode restarts recognition after every utterance; a task that keeps
/// failing at once must not spin the mic on and off.
final class RecognitionRestartTests: XCTestCase {
    func testNormalEndsRestartAtOnce() {
        var r = RecognitionRestarts()
        XCTAssertEqual(r.ended(after: 8, failed: false), 0, "an utterance finished")
        XCTAssertEqual(r.ended(after: 30, failed: true), 0, "no speech after a long silence")
        XCTAssertEqual(r.quickFailures, 0)
    }

    func testQuickFailuresBackOffAndDropOnDevice() {
        var r = RecognitionRestarts()
        XCTAssertEqual(r.ended(after: 0.01, failed: true), 0.25)
        XCTAssertEqual(r.ended(after: 0.01, failed: true), 0.5)
        XCTAssertTrue(r.onDeviceAllowed)
        XCTAssertEqual(r.ended(after: 0.01, failed: true), 1)
        XCTAssertFalse(r.onDeviceAllowed, "after a few, try server recognition")
        for _ in 0..<10 { _ = r.ended(after: 0.01, failed: true) }
        XCTAssertEqual(r.ended(after: 0.01, failed: true), 5, "capped")
    }

    func testHearingSpeechResetsTheBackoff() {
        var r = RecognitionRestarts()
        _ = r.ended(after: 0.01, failed: true)
        _ = r.ended(after: 0.01, failed: true)
        r.heard()
        XCTAssertEqual(r.ended(after: 0.01, failed: true), 0.25)
    }
}

@MainActor
final class SpeechPreferencesTests: XCTestCase {
    func testLoadsAndSavesVoicePreferencesThroughPlatform() {
        var platform = Platform.inert
        let saved = SpeechPreferences(voiceID: "sample-voice", speed: 0.85)
        platform.speech.loadPreferences = { saved }
        var written: SpeechPreferences?
        platform.speech.savePreferences = { written = $0 }
        let store = AppStore(platform: platform)
        XCTAssertEqual(store.state.speechPreferences, saved)

        let changed = SpeechPreferences(voiceID: "", speed: 1.2)
        store.setSpeechPreferences(changed)
        XCTAssertEqual(store.state.speechPreferences, changed)
        XCTAssertEqual(written, changed)
    }

    func testPreviewUsesSpeechWithoutRequestingMicrophone() {
        var platform = Platform.inert
        platform.speech.requestAccess = { XCTFail("A preview does not need microphone access") }
        var spoken = ""
        platform.speech.speak = { text, done in spoken = text; done() }
        let store = AppStore(platform: platform)
        store.previewVoice()
        XCTAssertFalse(spoken.isEmpty)
        XCTAssertFalse(store.state.drive.active)
    }
}

final class SpeechVoiceTests: XCTestCase {
    func testChoicesKeepTheReplyLanguageWithBetterVoicesFirst() {
        let all = [
            SpeechVoice(id: "fred", name: "Fred", language: "en-US", quality: ""),
            SpeechVoice(id: "thomas", name: "Thomas", language: "fr-FR", quality: "Enhanced"),
            SpeechVoice(id: "zoe", name: "Zoe", language: "en-US", quality: "Premium"),
            SpeechVoice(id: "serena", name: "Serena", language: "en-GB", quality: "Enhanced"),
            SpeechVoice(id: "ava", name: "Ava", language: "en-US", quality: "Premium"),
        ]
        XCTAssertEqual(SpeechVoice.choices(all, language: "en").map(\.id), ["ava", "zoe", "serena", "fred"])
        // No voice in the language: offer everything rather than nothing.
        XCTAssertEqual(SpeechVoice.choices(all, language: "ja").count, all.count)
    }

    func testRateFollowsSpeedWithinLimits() {
        func rate(_ speed: Double) -> Float {
            SpeechPreferences(speed: speed).rate(default: 0.5, min: 0, max: 1)
        }
        XCTAssertEqual(rate(1), 0.5, accuracy: 0.0001)
        XCTAssertEqual(rate(1.2), 0.6, accuracy: 0.0001)
        XCTAssertEqual(rate(9), 0.75, accuracy: 0.0001) // slider max 1.5×
        XCTAssertEqual(rate(0.1), 0.25, accuracy: 0.0001) // slider min 0.5×
        XCTAssertEqual(rate(.nan), 0.525, accuracy: 0.0001) // default 1.05×
        XCTAssertEqual(SpeechPreferences(speed: 1.5).rate(default: 0.8, min: 0, max: 1), 1)
    }
}

@MainActor
final class ChooseVoiceTests: XCTestCase {
    func testChoosingAVoiceSavesItAndPlaysASample() {
        var platform = Platform.inert
        var saved: SpeechPreferences?
        var spoken = 0
        platform.speech.savePreferences = { saved = $0 }
        platform.speech.speak = { _, done in spoken += 1; done() }
        let store = AppStore(platform: platform)
        store.chooseVoice("ava")
        XCTAssertEqual(saved?.voiceID, "ava")
        XCTAssertEqual(saved?.speed, SpeechPreferences().speed, "choosing a voice keeps the speed")
        XCTAssertEqual(store.state.speechPreferences.voiceID, "ava")
        XCTAssertEqual(spoken, 1)
    }
}
