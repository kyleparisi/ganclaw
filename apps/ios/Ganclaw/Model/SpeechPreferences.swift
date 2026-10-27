import Foundation

struct SpeechPreferences: Codable, Equatable {
    var voiceID = ""
    /// Multiplier of the system default rate, matching the original app.
    var speed: Double = 1.05

    static let speeds: ClosedRange<Double> = 0.5...1.5

    /// The synthesizer rate for `speed`, kept inside the slider's range and
    /// the synthesizer's own limits.
    func rate(default base: Float, min lower: Float, max upper: Float) -> Float {
        let speed = speed.isFinite ? Swift.min(Self.speeds.upperBound, Swift.max(Self.speeds.lowerBound, speed)) : 1.05
        return Swift.min(upper, Swift.max(lower, base * Float(speed)))
    }
}

struct SpeechVoice: Codable, Equatable, Identifiable {
    var id: String
    var name: String
    var language: String
    var quality: String

    var title: String { "\(name) · \(detail)" }
    var detail: String { quality.isEmpty ? language : "\(language) · \(quality)" }

    private var rank: Int { quality == "Premium" ? 0 : quality == "Enhanced" ? 1 : 2 }

    /// The voices worth offering for replies in `language` (a code such as
    /// "en"): only that language, the downloaded high-quality voices first.
    /// The device has dozens of voices in other languages and novelty
    /// voices; a flat list of all of them buries the good ones.
    static func choices(_ all: [SpeechVoice], language: String) -> [SpeechVoice] {
        let prefix = language.lowercased()
        let matching = all.filter {
            let code = $0.language.lowercased()
            return code == prefix || code.hasPrefix(prefix + "-")
        }
        return (matching.isEmpty ? all : matching).sorted {
            ($0.rank, $0.name.lowercased(), $0.language) < ($1.rank, $1.name.lowercased(), $1.language)
        }
    }
}
