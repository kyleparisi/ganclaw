import SwiftUI

/// Command selection fills the draft; the user sends it when ready.
struct SlashCommandSuggestions: View {
    var commands: [SlashCommand]
    var select: (SlashCommand) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(commands) { command in
                Button { select(command) } label: {
                    HStack(spacing: 10) {
                        Text("/" + command.name).font(.system(size: 13, weight: .semibold, design: .monospaced))
                        Text(command.description).font(.system(size: 13)).foregroundStyle(Theme.text2)
                        Spacer(minLength: 0)
                        #if os(macOS)
                        if command.id == commands.first?.id {
                            Text("Tab").font(.system(size: 11)).foregroundStyle(Theme.muted)
                        }
                        #endif
                    }
                    .padding(.horizontal, 12).padding(.vertical, 8)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("/\(command.name), \(command.description)")
            }
        }
        .background(Theme.card, in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(Theme.line))
        .padding(.horizontal, 12).padding(.vertical, 6)
    }
}
