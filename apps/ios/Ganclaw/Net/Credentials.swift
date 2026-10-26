import Foundation

/// The server and device token this phone is paired with.
struct Credentials: Codable, Equatable, Sendable {
    var server: URL
    var token: String
    var deviceName: String
}
