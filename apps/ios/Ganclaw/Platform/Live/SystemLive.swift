import Foundation
import Security
import UIKit
import UniformTypeIdentifiers

extension PasteboardClient {
    static var live: PasteboardClient {
        PasteboardClient(
            string: { UIPasteboard.general.string },
            setString: { UIPasteboard.general.string = $0 },
            pastedImage: {
                let pb = UIPasteboard.general
                guard pb.hasImages else { return nil }
                return PastedImage.make(png: pb.data(forPasteboardType: UTType.png.identifier), image: pb.image)
            },
            url: { UIPasteboard.general.url },
            hasContent: { UIPasteboard.general.hasImages || UIPasteboard.general.hasURLs || UIPasteboard.general.hasStrings },
            hasImages: { UIPasteboard.general.hasImages }
        )
    }
}

extension DeviceClient {
    static func live(check: CapabilityCheck) -> DeviceClient {
        DeviceClient(
            name: { UIDevice.current.name },
            setIdleTimerDisabled: { UIApplication.shared.isIdleTimerDisabled = $0 },
            cameraAvailable: {
                check.missing(.camera) == nil && UIImagePickerController.isSourceTypeAvailable(.camera)
            }
        )
    }
}

extension KeychainClient {
    /// The Keychain, readable only on this device. Builds without a signing
    /// team (the ad-hoc Mac app) can't use it, so they fall back to a 0600
    /// file in Application Support.
    static var live: KeychainClient { KeychainClient(
        load: {
            var out: CFTypeRef?
            if SecItemCopyMatching(query(returning: true) as CFDictionary, &out) == errSecSuccess, let data = out as? Data {
                return try? JSONDecoder().decode(Credentials.self, from: data)
            }
            guard let data = try? Data(contentsOf: fallbackURL) else { return nil }
            return try? JSONDecoder().decode(Credentials.self, from: data)
        },
        save: { c in
            guard let data = try? JSONEncoder().encode(c) else { return }
            clearAll()
            var q = query(returning: false)
            q[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
            q[kSecValueData as String] = data
            if SecItemAdd(q as CFDictionary, nil) != errSecSuccess {
                let dir = fallbackURL.deletingLastPathComponent()
                try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
                try? data.write(to: fallbackURL, options: [.atomic, .completeFileProtection])
                try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: fallbackURL.path)
            }
        },
        clear: { clearAll() }
    ) }

    private nonisolated static func query(returning: Bool) -> [String: Any] {
        var q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: "com.kyleparisi.ganclaw",
            kSecAttrAccount as String: "pairing",
        ]
        if returning {
            q[kSecReturnData as String] = true
            q[kSecMatchLimit as String] = kSecMatchLimitOne
        }
        return q
    }

    private nonisolated static func clearAll() {
        SecItemDelete(query(returning: false) as CFDictionary)
        try? FileManager.default.removeItem(at: fallbackURL)
    }

    private nonisolated static var fallbackURL: URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        return base.appendingPathComponent("Ganclaw", isDirectory: true).appendingPathComponent("pairing.json")
    }
}
