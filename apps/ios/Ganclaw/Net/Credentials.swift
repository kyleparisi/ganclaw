import Foundation
import Security

/// The server and device token this phone is paired with.
struct Credentials: Codable, Equatable {
    var server: URL
    var token: String
    var deviceName: String
}

/// Persists credentials in the Keychain, readable only on this device.
/// Builds without a signing team (the ad-hoc Mac app) can't use the
/// Keychain, so they fall back to a 0600 file in Application Support.
enum CredentialStore {
    private static let service = "com.kyleparisi.ganclaw"
    private static let account = "pairing"

    static func load() -> Credentials? {
        let q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var out: CFTypeRef?
        if SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess, let data = out as? Data {
            return try? JSONDecoder().decode(Credentials.self, from: data)
        }
        guard let data = try? Data(contentsOf: fallbackURL) else { return nil }
        return try? JSONDecoder().decode(Credentials.self, from: data)
    }

    static func save(_ c: Credentials) {
        guard let data = try? JSONEncoder().encode(c) else { return }
        clear()
        let q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
            kSecValueData as String: data,
        ]
        if SecItemAdd(q as CFDictionary, nil) != errSecSuccess {
            let dir = fallbackURL.deletingLastPathComponent()
            try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            try? data.write(to: fallbackURL, options: [.atomic, .completeFileProtection])
            try? FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: fallbackURL.path)
        }
    }

    private static var fallbackURL: URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
        return base.appendingPathComponent("Ganclaw", isDirectory: true).appendingPathComponent("pairing.json")
    }

    static func clear() {
        let q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        SecItemDelete(q as CFDictionary)
        try? FileManager.default.removeItem(at: fallbackURL)
    }
}
