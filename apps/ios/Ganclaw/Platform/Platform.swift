import Foundation
import UIKit

// The app's only door to the device. Like the clients in investor-be's
// server (SNSClient, SendGridClient): each client is a struct of closures,
// `.live` binds the real Apple API, and tests build one with stub closures
// inline. Nothing outside Platform/ imports AVFoundation, Speech or
// Security or touches UIPasteboard/UIDevice directly.
//
// Live clients that touch a privacy-sensitive device are built from a
// Capability (Capability.swift) and check it before every use, so a build
// that lacks a declaration fails with a PlatformError instead of the OS
// terminating the app.

@MainActor
struct Platform {
    var speech: SpeechClient
    var recorder: RecorderClient
    var pasteboard: PasteboardClient
    var keychain: KeychainClient
    var notifications: NotificationClient = .inert
    var device: DeviceClient

    static func live(check: CapabilityCheck = .live) -> Platform {
        Platform(
            speech: .live(check: check),
            recorder: .live(check: check),
            pasteboard: .live,
            keychain: .live,
            notifications: .live(check: check),
            device: .live(check: check)
        )
    }
}

/// Drive mode: listen continuously, speak replies, let the user talk over
/// them. `listen` reports the transcript so far, then `final: true` after a
/// pause.
@MainActor
struct SpeechClient {
    var requestAccess: () async throws -> Void
    var listen: (_ onHeard: @escaping @MainActor (_ text: String, _ final: Bool) -> Void) throws -> Void
    var stopListening: () -> Void
    var speak: (_ text: String, _ done: @escaping @MainActor () -> Void) -> Void
    var stopSpeaking: () -> Void
    var voices: () -> [SpeechVoice] = { [] }
    var loadPreferences: () -> SpeechPreferences = { SpeechPreferences() }
    var savePreferences: (SpeechPreferences) -> Void = { _ in }
    var isSpeaking: () -> Bool
}

/// Voice notes, recorded as AAC.
@MainActor
struct RecorderClient {
    var requestAccess: () async throws -> Void
    var start: () throws -> Void
    /// Stops and returns the recording (nil if under half a second).
    var stop: () -> Data?
    var cancel: () -> Void
    var elapsed: () -> TimeInterval
}

@MainActor
struct PasteboardClient {
    var string: () -> String?
    var setString: (String) -> Void
    /// The clipboard's image as upload bytes (see `PastedImage.make`).
    var pastedImage: () -> PastedImage?
    var url: () -> URL?
    var hasContent: () -> Bool
    var hasImages: () -> Bool = { false }
}

/// Stores the pairing (server + device token).
@MainActor
struct KeychainClient {
    var load: () -> Credentials?
    var save: (Credentials) -> Void
    var clear: () -> Void
}

@MainActor
struct DeviceClient {
    var name: () -> String
    var setIdleTimerDisabled: (Bool) -> Void
    /// Whether the camera can be offered (present, and this build may use it).
    var cameraAvailable: () -> Bool
}

// MARK: - Inert clients for tests and previews

extension Platform {
    /// Does nothing and stores nothing; override the parts a test needs,
    /// e.g. `var p = Platform.inert; p.pasteboard.setString = { copied = $0 }`.
    static var inert: Platform {
        Platform(speech: .inert, recorder: .inert, pasteboard: .inert, keychain: .inert, device: .inert)
    }
}

extension SpeechClient {
    static var inert: SpeechClient {
        SpeechClient(requestAccess: {}, listen: { _ in }, stopListening: {}, speak: { _, done in done() }, stopSpeaking: {}, isSpeaking: { false })
    }
}

extension RecorderClient {
    static var inert: RecorderClient {
        RecorderClient(requestAccess: {}, start: {}, stop: { nil }, cancel: {}, elapsed: { 0 })
    }
}

extension PasteboardClient {
    static var inert: PasteboardClient {
        PasteboardClient(string: { nil }, setString: { _ in }, pastedImage: { nil }, url: { nil }, hasContent: { false })
    }
}

extension KeychainClient {
    static var inert: KeychainClient { KeychainClient(load: { nil }, save: { _ in }, clear: {}) }
}

extension DeviceClient {
    static var inert: DeviceClient {
        DeviceClient(name: { "Test device" }, setIdleTimerDisabled: { _ in }, cameraAvailable: { false })
    }
}

/// Permission is requested only from the Notifications setting, after pairing.
@MainActor
struct NotificationClient {
    var setBadge: (Int) async -> Void = { _ in }
    var enable: () async throws -> Void
    var registerIfAuthorized: () async -> Bool
    var observe: (@escaping @MainActor (String) -> Void, @escaping @MainActor (String) -> Void, @escaping @MainActor (String) -> Void) -> Void
    static var inert: NotificationClient {
        NotificationClient(enable: {}, registerIfAuthorized: { false }, observe: { _, _, _ in })
    }
}
