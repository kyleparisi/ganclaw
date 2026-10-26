import Foundation

/// A message body split into prose and fenced code blocks.
enum Segment: Equatable {
    case prose(String)
    /// A fenced block. `info` is the fence's label, e.g. "subject", "shell".
    case code(info: String, text: String)

    static func parse(_ text: String) -> [Segment] {
        var out: [Segment] = []
        var prose: [String] = []
        var code: [String]?
        var info = ""
        func flushProse() {
            let p = prose.joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
            if !p.isEmpty { out.append(.prose(p)) }
            prose = []
        }
        for line in text.components(separatedBy: "\n") {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if trimmed.hasPrefix("```") {
                if let c = code {
                    out.append(.code(info: info, text: c.joined(separator: "\n")))
                    code = nil
                } else {
                    flushProse()
                    info = String(trimmed.dropFirst(3)).trimmingCharacters(in: .whitespaces)
                    code = []
                }
            } else if code != nil {
                code!.append(line)
            } else {
                prose.append(line)
            }
        }
        if let c = code { out.append(.code(info: info, text: c.joined(separator: "\n"))) } // unclosed while streaming
        flushProse()
        return out
    }
}
