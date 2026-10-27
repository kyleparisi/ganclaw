import Foundation

struct SpeechPreferences: Codable, Equatable {
    var voiceID = ""
    /// Multiplier of the system default rate, matching the original app.
    var speed: Double = 1.05
}

struct SpeechVoice: Equatable, Identifiable {
    var id: String
    var name: String
    var language: String
    var quality: String

    var title: String { "\(name) · \(language)\(quality.isEmpty ? "" : " · " + quality)" }
}
