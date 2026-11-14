import SwiftUI

struct MessageRow: View {
    var message: AppMessage
    var streamed: String?
    var replyTo: AppMessage?
    /// Widest a bubble grows: phone-sized on iOS, wider in the Mac window.
    var bubbleWidth: CGFloat = 310
    var onReply: () -> Void

    @Environment(AppStore.self) private var store
    /// Tracks the finger without animation; only the snap back on release animates.
    @GestureState(resetTransaction: Transaction(animation: .easeOut(duration: 0.18)))
    private var swipeTranslation: CGSize = .zero

    /// System notices are status lines, not something to answer.
    private var canReply: Bool { message.status != .streaming && message.role != .system }
    private var swipeOffset: CGFloat { canReply ? ReplySwipe.offset(swipeTranslation) : 0 }
    private var swipeArmed: Bool { canReply && ReplySwipe.selectsReply(swipeTranslation) }

    var body: some View {
        ZStack(alignment: .trailing) {
            Image(systemName: "arrowshape.turn.up.left.fill")
                .font(.system(size: 16, weight: .semibold))
                .foregroundStyle(Theme.blue)
                .frame(width: 36, height: 36)
                .background(Theme.card, in: Circle())
                .padding(.trailing, 12)
                .opacity(min(-swipeOffset / 64, 1))
                .accessibilityHidden(true)
            content.offset(x: swipeOffset)
        }
        .contentShape(Rectangle())
        #if os(iOS)
        .simultaneousGesture(
            DragGesture(minimumDistance: 20)
                .updating($swipeTranslation) { value, state, _ in state = value.translation }
                .onEnded { selectReply($0.translation) }
        )
        // Tick when a release would select the reply.
        .sensoryFeedback(.impact(weight: .light), trigger: swipeArmed) { _, armed in armed }
        #endif
        // On the Mac a drag selects text, so replying is the context menu
        // or the hover button instead of a swipe.
        .contextMenu {
            if canReply {
                Button("Reply", systemImage: "arrowshape.turn.up.left", action: onReply)
            }
            // The row's long-press menu can preempt text selection, so copying stays here too.
            if !message.text.isEmpty, message.status != .streaming {
                Button("Copy", systemImage: "doc.on.doc") {
                    store.copy(message.text, block: "message.\(message.id)", label: "Message")
                }
            }
        }
    }

    private func selectReply(_ translation: CGSize) {
        if canReply && ReplySwipe.selectsReply(translation) { onReply() }
    }

    @ViewBuilder private var content: some View {
        switch message.role {
        case .user: user
        case .agent: agent
        case .system: system
        }
    }

    private var user: some View {
        VStack(alignment: .trailing, spacing: 6) {
            ForEach(message.files) { FileView(file: $0, onUser: true) }
            if let r = replyTo, !r.actions.contains(message.text) {
                Text(r.replyPreview).font(.system(size: 12)).foregroundStyle(Theme.muted).lineLimit(1)
                    .padding(.horizontal, 10).padding(.vertical, 4)
                    .overlay(alignment: .leading) { Theme.line.frame(width: 2) }
            }
            if !message.text.isEmpty {
                MessageContent(text: message.text, messageID: message.id, onUser: true)
                    .padding(.horizontal, 14).padding(.vertical, 10)
                    .background(Theme.blue, in: UnevenRoundedRectangle(topLeadingRadius: 18, bottomLeadingRadius: 18, bottomTrailingRadius: 4, topTrailingRadius: 18, style: .continuous))
            }
        }
        .frame(maxWidth: bubbleWidth - 10, alignment: .trailing)
        .frame(maxWidth: .infinity, alignment: .trailing)
        .accessibilityElement(children: .contain)
        .accessibilityAction(named: Text("Reply")) { if canReply { onReply() } }
        .control("message.\(message.id)", swipe: selectReply)
    }

    private var agent: some View {
        let text = message.status == .streaming ? (streamed ?? message.text) : message.text
        return VStack(alignment: .leading, spacing: 8) {
            if message.job != nil {
                Label("Scheduled · \(message.job!)", systemImage: "clock")
                    .font(.system(size: 12, weight: .medium)).foregroundStyle(Theme.muted)
            }
            if !text.isEmpty || message.status == .streaming {
                Group {
                    if text.isEmpty {
                        TypingDots()
                    } else {
                        MessageContent(text: text, messageID: message.id)
                    }
                }
                .padding(.horizontal, 14).padding(.vertical, 10)
                .background(Theme.card, in: UnevenRoundedRectangle(topLeadingRadius: 18, bottomLeadingRadius: 4, bottomTrailingRadius: 18, topTrailingRadius: 18, style: .continuous))
                .overlay(UnevenRoundedRectangle(topLeadingRadius: 18, bottomLeadingRadius: 4, bottomTrailingRadius: 18, topTrailingRadius: 18, style: .continuous)
                    .stroke(message.status == .error ? Theme.red.opacity(0.5) : Theme.line))
            }
            ForEach(message.files) { FileView(file: $0, onUser: false) }
            if !message.actions.isEmpty {
                ActionCard(message: message)
            }
        }
        .frame(maxWidth: bubbleWidth, alignment: .leading)
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .contain)
        .accessibilityAction(named: Text("Reply")) { if canReply { onReply() } }
        .control("message.\(message.id)", swipe: selectReply)
    }

    private var system: some View {
        Text(message.text)
            .font(.system(size: 13))
            .foregroundStyle(Theme.muted)
            .multilineTextAlignment(.center)
            .padding(.horizontal, 24)
            .frame(maxWidth: .infinity)
            .control("message.\(message.id)")
    }
}

/// Reply buttons from a "BUTTONS:" line.
struct ActionCard: View {
    var message: AppMessage
    @Environment(AppStore.self) private var store

    var body: some View {
        Group {
            if let answer = message.answer, !answer.isEmpty {
                Label(answer, systemImage: "checkmark.circle.fill")
                    .font(.system(size: 13, weight: .semibold))
                    .foregroundStyle(Theme.teal)
                    .padding(.horizontal, 12).frame(height: 32)
                    .background(Color(hex: 0xE5F3F1), in: Capsule())
                    .control("answer.\(message.id)")
            } else {
                HStack(spacing: 8) {
                    ForEach(Array(message.actions.enumerated()), id: \.offset) { i, label in
                        CButton("action.\(message.id).\(label)") { Task { await store.choose(label, on: message) } } label: {
                            Text(label)
                                .font(.system(size: 14, weight: i == 0 ? .semibold : .medium))
                                .frame(maxWidth: .infinity).frame(height: 44)
                                .background(i == 0 ? Theme.ink : Theme.card, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                                .overlay(RoundedRectangle(cornerRadius: 12, style: .continuous).stroke(i == 0 ? .clear : Theme.line))
                                .foregroundStyle(i == 0 ? .white : Theme.ink)
                        }
                        .buttonStyle(.plain)
                    }
                }
                .padding(10)
                .background(Theme.card, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 16, style: .continuous).stroke(Theme.line))
            }
        }
    }
}

struct TypingDots: View {
    @State private var on = false
    var body: some View {
        HStack(spacing: 4) {
            ForEach(0..<3) { i in
                Circle().fill(Theme.muted).frame(width: 6, height: 6)
                    .opacity(on ? 1 : 0.3)
                    .animation(.easeInOut(duration: 0.6).repeatForever().delay(Double(i) * 0.2), value: on)
            }
        }
        .frame(height: 20)
        .onAppear { on = true }
        .accessibilityLabel("Writing")
    }
}

struct TypingBubble: View {
    var body: some View {
        TypingDots()
            .padding(.horizontal, 14).padding(.vertical, 10)
            .background(Theme.card, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(Theme.line))
            .control("thread.typing")
    }
}
