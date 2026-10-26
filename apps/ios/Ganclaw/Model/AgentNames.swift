import Foundation

/// How agent names are shown: "log-checker" → "Log Checker", "LC".
enum AgentNames {
    static func initials(_ name: String) -> String {
        let parts = name.split(whereSeparator: { $0 == " " || $0 == "-" || $0 == "_" })
        if parts.count >= 2 { return (String(parts[0].prefix(1)) + String(parts[1].prefix(1))).uppercased() }
        let letters = name.filter(\.isLetter)
        guard let first = letters.first else { return "?" }
        // Second letter: the first consonant after the first letter reads
        // better than the plain second letter (Research → RS, Infra → IN).
        let rest = letters.dropFirst()
        let second = rest.first(where: { !"aeiouAEIOU".contains($0) }) ?? rest.first ?? first
        return (String(first) + String(second)).uppercased()
    }

    static func displayName(_ name: String) -> String {
        name.split(whereSeparator: { $0 == "-" || $0 == "_" }).map { $0.prefix(1).uppercased() + $0.dropFirst() }.joined(separator: " ")
    }
}
