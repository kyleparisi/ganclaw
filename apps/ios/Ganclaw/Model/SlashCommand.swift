import Foundation

struct SlashCommand: Codable, Equatable, Identifiable {
    var name: String
    var description: String
    var id: String { name }

    /// Used with servers that predate the commands endpoint.
    static let builtIn: [SlashCommand] = [
        .init(name: "new", description: "Start a new conversation"),
        .init(name: "reset", description: "Clear the conversation (same as /new)"),
        .init(name: "stop", description: "Stop the reply in progress"),
        .init(name: "status", description: "Show model availability and limits"),
        .init(name: "help", description: "List commands"),
    ]

    static func suggestions(for draft: String, commands: [SlashCommand]) -> [SlashCommand] {
        guard draft.hasPrefix("/"), !draft.contains(where: \.isWhitespace) else { return [] }
        let query = String(draft.dropFirst()).lowercased()
        // A completed command is ready to send, so close the suggestions.
        guard !commands.contains(where: { $0.name.lowercased() == query }) else { return [] }
        return commands.filter { $0.name.lowercased().hasPrefix(query) }
    }
}
