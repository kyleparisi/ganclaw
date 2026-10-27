import Foundation

// The capability manifest: every privacy-sensitive thing the app touches,
// with what the OS demands before it may be used. This file is the single
// source of truth:
//
//  - tools/gencap generates Info.plist usage strings and the Mac
//    entitlements file from it on every build (see project.yml), and CI
//    checks the signed app carries exactly these entitlements;
//  - live platform clients (Platform/Live) are only built through a
//    capability, and check at run time that the running binary really has
//    it, so a missing declaration is an error message instead of the OS
//    killing the app.
//
// Foundation-only so the generator can compile it on its own.

enum Capability: String, CaseIterable, Codable, Sendable {
    case microphone
    case speechRecognition
    case camera
    case photoLibrary
    case notifications

    /// Info.plist key whose string iOS and macOS show in the permission
    /// prompt. Using the API without it terminates the app.
    var usageKey: String {
        switch self {
        case .microphone: "NSMicrophoneUsageDescription"
        case .speechRecognition: "NSSpeechRecognitionUsageDescription"
        case .camera: "NSCameraUsageDescription"
        case .photoLibrary: "NSPhotoLibraryUsageDescription"
        case .notifications: "" // Authorization is managed by UserNotifications, no usage string.
        }
    }

    var usageDescription: String {
        switch self {
        case .microphone: "Talk to your agents in drive mode and record voice notes."
        case .speechRecognition: "Turn what you say in drive mode into messages for your agents."
        case .camera: "Take a photo to send to an agent."
        case .photoLibrary: "Choose photos to send to an agent."
        case .notifications: ""
        }
    }

    /// Entitlement the Mac app needs under the hardened runtime. Without it
    /// macOS kills the process the moment the device is opened, even after
    /// the user said yes. iOS has no equivalent.
    var macEntitlement: String? {
        switch self {
        case .microphone: "com.apple.security.device.audio-input"
        case .camera: "com.apple.security.device.camera"
        // Speech recognition records through the microphone entitlement;
        // the photo picker runs out of process and needs none.
        case .speechRecognition, .photoLibrary, .notifications: nil
        }
    }

    /// Capabilities this one can't work without. Speech recognition reads
    /// the microphone, so it needs the microphone's declarations too.
    var requires: [Capability] {
        switch self {
        case .speechRecognition: [.microphone]
        default: []
        }
    }

    /// iOS remote notification signing entitlement. Xcode expands the build setting.
    static var iosEntitlements: [String: String] {
        ["aps-environment": "$(APNS_ENVIRONMENT)"]
    }

    /// Every entitlement the Mac build must be signed with.
    static var macEntitlements: [String] {
        Array(Set(allCases.compactMap(\.macEntitlement))).sorted()
    }
}

/// Why a platform client refused to run.
enum PlatformError: LocalizedError, Equatable {
    /// The build is missing a declaration the OS requires; using the API
    /// anyway would get the app killed. A build problem, not a user one.
    case undeclared(Capability, missing: String)
    /// The user said no (or a device policy did).
    case denied(Capability)
    case unavailable(String)

    var errorDescription: String? {
        switch self {
        case let .undeclared(c, missing):
            "This build can't use the \(c.title): it is missing \(missing). Rebuild the app (tools/gencap)."
        case .denied(let c):
            "Allow \(c.title) access for Ganclaw in Settings."
        case .unavailable(let why):
            why
        }
    }
}

extension Capability {
    var title: String {
        switch self {
        case .microphone: "microphone"
        case .speechRecognition: "speech recognition"
        case .camera: "camera"
        case .photoLibrary: "photo library"
        case .notifications: "notifications"
        }
    }
}
