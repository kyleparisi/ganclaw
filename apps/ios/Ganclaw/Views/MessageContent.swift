import SwiftUI

/// Code languages shown dark, like a terminal; anything else is treated as
/// text to paste (a subject, an email body) and shown light.
private let darkLanguages: Set<String> = ["shell", "sh", "bash", "zsh", "console", "go", "swift", "python", "py", "js", "ts", "json", "yaml", "sql", "diff", "ruby", "rust", "c", "cpp", "java", "kotlin", "html", "css", "toml", "dockerfile", "make"]

struct MessageContent: View {
    var text: String
    var messageID: Int64
    var onUser = false

    @Environment(AppStore.self) private var store

    var body: some View {
        let segments = Segment.parse(text)
        VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(segments.enumerated()), id: \.offset) { i, seg in
                switch seg {
                case .prose(let p):
                    Text(markdown(p))
                        .font(.system(size: 15))
                        .lineSpacing(3)
                        .foregroundStyle(onUser ? .white : Theme.ink)
                        .tint(onUser ? .white : Theme.blue)
                        .textSelection(.enabled)
                        .fixedSize(horizontal: false, vertical: true)
                case let .code(info, code):
                    CopyBlock(info: info, text: code, blockID: "\(messageID).\(i)")
                }
            }
        }
    }

    private func markdown(_ s: String) -> AttributedString {
        (try? AttributedString(markdown: s, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace))) ?? AttributedString(s)
    }
}

/// The design's copy card: a label, the content and a Copy button that
/// briefly turns into "Copied".
struct CopyBlock: View {
    var info: String
    var text: String
    var blockID: String

    @Environment(AppStore.self) private var store

    private var lang: String { info.lowercased() }
    private var dark: Bool { darkLanguages.contains(lang) }
    private var label: String {
        if lang.isEmpty { return "Code" }
        if dark { return lang }
        return lang.prefix(1).uppercased() + lang.dropFirst()
    }

    var body: some View {
        let copied = store.state.copiedBlock == blockID
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text(dark ? label : (lang == "body" ? "Body · plain text" : label))
                    .font(.system(size: 12, weight: .semibold, design: dark ? .monospaced : .default))
                    .foregroundStyle(dark ? Color(hex: 0x9CA3AF) : Theme.muted)
                Spacer()
                Button {
                    store.copy(text, block: blockID, label: dark ? "Command" : label)
                } label: {
                    HStack(spacing: 4) {
                        Image(systemName: copied ? "checkmark" : "doc.on.doc").font(.system(size: 12, weight: .semibold))
                        Text(copied ? "Copied" : "Copy").font(.system(size: 13, weight: .semibold))
                    }
                    .padding(.horizontal, 12).frame(minHeight: 32)
                    .background(copied ? Theme.teal : (dark ? Color(hex: 0x2E3138) : Theme.card), in: Capsule())
                    .overlay(Capsule().stroke(copied || dark ? .clear : Theme.line))
                    .foregroundStyle(copied || dark ? .white : Theme.ink)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(copied ? "Copied \(label)" : "Copy \(label)")
                .accessibilityIdentifier("copy.\(blockID)")
            }
            Text(text)
                .font(.system(size: dark ? 13 : 15, design: dark ? .monospaced : .default))
                .foregroundStyle(dark ? Theme.codeFg : Theme.ink)
                .lineSpacing(2)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(12)
        .background(dark ? Theme.codeBg : Theme.ground, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 14, style: .continuous).stroke(dark ? .clear : Theme.lineSoft))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("block.\(blockID)")
    }
}
