import SwiftUI

struct ThreadView: View {
    var agent: String
    @Environment(AppStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    @State private var recorder = VoiceRecorder()
    @FocusState private var composerFocused: Bool

    var body: some View {
        let t = store.state.thread(agent)
        VStack(spacing: 0) {
            header
            messages(t)
            if let rid = t.replyTo { replyBar(rid, t) }
            composer(t)
        }
        .background(Theme.ground.ignoresSafeArea())
        .toolbar(.hidden, for: .navigationBar)
        .accessibilityIdentifier("screen.thread")
        .task(id: agent) { if !t.loaded { await store.loadThread(agent) } }
    }

    // MARK: Header

    private var header: some View {
        let summary = store.state.agent(agent)
        let st = summary?.state ?? .idle
        return HStack(spacing: 8) {
            Button { store.send(.pop) } label: {
                Image(systemName: "chevron.left").font(.system(size: 20, weight: .semibold)).frame(width: 44, height: 44)
            }
            .foregroundStyle(Theme.ink)
            .accessibilityLabel("Back to agents")
            .accessibilityIdentifier("thread.back")
            AgentAvatar(name: agent, size: 36)
            VStack(alignment: .leading, spacing: 1) {
                Text(Theme.displayName(agent)).font(.system(size: 16, weight: .semibold))
                Text(st.working ? "Working · \((st.detail ?? "working").lowercased())" : statusLine(summary))
                    .font(.system(size: 12, weight: st.working ? .medium : .regular))
                    .foregroundStyle(st.working ? Theme.blue : Theme.muted)
                    .accessibilityIdentifier("thread.status")
            }
            Spacer()
            Menu {
                if st.working {
                    Button("Stop", systemImage: "stop.circle") { Task { await store.stop(agent) } }
                }
                Button("New conversation", systemImage: "arrow.counterclockwise") { Task { await store.reset(agent) } }
            } label: {
                Image(systemName: "ellipsis").font(.system(size: 17, weight: .semibold)).frame(width: 40, height: 40)
            }
            .foregroundStyle(Theme.ink)
            .accessibilityLabel("Thread options")
            .accessibilityIdentifier("thread.menu")
            Button { Task { await store.startDrive(agent) } } label: {
                HStack(spacing: 6) {
                    Image(systemName: "headphones").foregroundStyle(Theme.amber)
                    Text("Audio").font(.system(size: 13, weight: .semibold))
                }
                .padding(.horizontal, 14).frame(height: 40)
                .background(Theme.ink, in: Capsule())
                .foregroundStyle(.white)
            }
            .accessibilityLabel("Switch to drive mode")
            .accessibilityIdentifier("thread.audio")
        }
        .padding(.horizontal, 12).padding(.vertical, 8)
        .overlay(alignment: .bottom) { Theme.line.frame(height: 1) }
    }

    private func statusLine(_ s: AgentSummary?) -> String {
        guard let last = s?.lastMessage else { return "Idle" }
        return "Idle · last active \(last.createdAt.shortAgo())"
    }

    // MARK: Messages

    private func messages(_ t: ThreadState) -> some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 10) {
                    if t.loading && t.messages.isEmpty {
                        ProgressView().padding(.top, 40)
                    }
                    if let e = t.error, t.messages.isEmpty {
                        Text(e).font(.system(size: 14)).foregroundStyle(Theme.muted).padding(.top, 40)
                    }
                    if t.loaded && t.messages.isEmpty {
                        Text("Say hello to \(Theme.displayName(agent)).").font(.system(size: 15)).foregroundStyle(Theme.muted).padding(.top, 40)
                            .accessibilityIdentifier("thread.empty")
                    }
                    ForEach(Array(t.messages.enumerated()), id: \.element.id) { i, m in
                        if i == 0 || !Calendar.current.isDate(t.messages[i - 1].createdAt, equalTo: m.createdAt, toGranularity: .hour) {
                            Text(dayLabel(m.createdAt)).font(.system(size: 12, weight: .medium)).foregroundStyle(Theme.muted).padding(.top, 4)
                        }
                        MessageRow(message: m, streamed: t.streaming[m.id], replyTo: m.replyTo.flatMap { t.message($0) })
                            .id(m.id)
                    }
                    if store.state.agent(agent)?.state.working == true, t.streaming.isEmpty, t.messages.last?.role == .user {
                        TypingBubble().frame(maxWidth: .infinity, alignment: .leading).id("typing")
                    }
                    Color.clear.frame(height: 4).id("bottom")
                }
                .padding(.horizontal, 14)
                .padding(.top, 14)
            }
            .scrollDismissesKeyboard(.interactively)
            .defaultScrollAnchor(.bottom)
            .onChange(of: t.messages.last?.id) { _, _ in withAnimation { proxy.scrollTo("bottom", anchor: .bottom) } }
            .onChange(of: t.streaming) { _, _ in proxy.scrollTo("bottom", anchor: .bottom) }
            .accessibilityIdentifier("thread.messages")
        }
    }

    private func dayLabel(_ d: Date) -> String {
        let time = d.formatted(date: .omitted, time: .shortened)
        if Calendar.current.isDateInToday(d) { return "Today \(time)" }
        if Calendar.current.isDateInYesterday(d) { return "Yesterday \(time)" }
        return d.formatted(.dateTime.month(.abbreviated).day().hour().minute())
    }

    // MARK: Composer

    private func replyBar(_ id: Int64, _ t: ThreadState) -> some View {
        HStack(spacing: 8) {
            Theme.blue.frame(width: 3).clipShape(Capsule())
            VStack(alignment: .leading, spacing: 1) {
                Text("Replying to").font(.system(size: 12, weight: .semibold)).foregroundStyle(Theme.blue)
                Text(t.message(id)?.text ?? "a message").font(.system(size: 13)).foregroundStyle(Theme.text2).lineLimit(1)
            }
            Spacer()
            Button { store.send(.replyTo(agent, nil)) } label: { Image(systemName: "xmark").frame(width: 36, height: 36) }
                .foregroundStyle(Theme.text2)
                .accessibilityLabel("Cancel reply")
                .accessibilityIdentifier("thread.cancelReply")
        }
        .frame(height: 44)
        .padding(.horizontal, 14)
        .background(Theme.card)
        .accessibilityIdentifier("thread.replyBar")
    }

    private func composer(_ t: ThreadState) -> some View {
        HStack(alignment: .bottom, spacing: 8) {
            Button { store.send(.attachOpen(agent)) } label: {
                Image(systemName: "plus").font(.system(size: 18, weight: .semibold))
                    .frame(width: 44, height: 44)
                    .background(Theme.card, in: Circle())
                    .overlay(Circle().stroke(Theme.line))
                    .foregroundStyle(Theme.ink)
            }
            .accessibilityLabel("Add attachment")
            .accessibilityIdentifier("composer.attach")

            HStack(alignment: .bottom, spacing: 4) {
                if recorder.recording {
                    HStack(spacing: 8) {
                        Circle().fill(Theme.red).frame(width: 8, height: 8)
                        Text("Recording \(recorder.elapsedText)").font(.system(size: 15, design: .monospaced)).foregroundStyle(Theme.ink)
                        Spacer()
                        Button("Cancel") { recorder.cancel() }.font(.system(size: 14, weight: .medium))
                            .accessibilityIdentifier("composer.cancelRecording")
                    }
                    .padding(.leading, 16).frame(minHeight: 42)
                } else {
                    TextField("Message \(Theme.displayName(agent))", text: Binding(get: { store.state.thread(agent).draft }, set: { store.send(.draft(agent, $0)) }), axis: .vertical)
                        .font(.system(size: 15))
                        .lineLimit(1...6)
                        .padding(.leading, 16).padding(.vertical, 11)
                        .focused($composerFocused)
                        .accessibilityLabel("Message")
                        .accessibilityIdentifier("composer.text")
                }
                Button { toggleRecording() } label: {
                    Image(systemName: recorder.recording ? "stop.fill" : "mic")
                        .font(.system(size: 18))
                        .frame(width: 36, height: 42)
                        .foregroundStyle(recorder.recording ? Theme.red : Theme.text2)
                }
                .accessibilityLabel(recorder.recording ? "Stop and send voice note" : "Record voice note")
                .accessibilityIdentifier("composer.mic")
                .padding(.trailing, 4)
            }
            .background(Theme.card, in: RoundedRectangle(cornerRadius: 22, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 22, style: .continuous).stroke(Theme.line))

            Button { Task { await store.sendDraft(agent) } } label: {
                Image(systemName: "arrow.up").font(.system(size: 18, weight: .bold))
                    .frame(width: 44, height: 44)
                    .background(canSend(t) ? Theme.blue : Theme.blue.opacity(0.4), in: Circle())
                    .foregroundStyle(.white)
            }
            .disabled(!canSend(t))
            .accessibilityLabel("Send")
            .accessibilityIdentifier("composer.send")
        }
        .padding(.horizontal, 12).padding(.top, 8).padding(.bottom, 8)
        .background(Theme.ground)
        .overlay(alignment: .top) { Theme.line.frame(height: 1) }
    }

    private func canSend(_ t: ThreadState) -> Bool {
        !t.sending && !t.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    private func toggleRecording() {
        if recorder.recording {
            guard let data = recorder.stop() else { return }
            let agent = self.agent
            Task {
                guard let api = store.api else { return }
                do {
                    let f = try await api.upload(agent: agent, name: "voice-note.m4a", mime: "audio/mp4", data: data)
                    await store.sendMessage(agent, text: "", fileIDs: [f.id], replyTo: store.state.thread(agent).replyTo, voice: false)
                } catch {
                    store.send(.banner(store.describe(error)))
                }
            }
        } else {
            Task {
                if !(await recorder.start()) { store.send(.banner("Allow the microphone in Settings to record voice notes.")) }
            }
        }
    }
}

struct MessageRow: View {
    var message: AppMessage
    var streamed: String?
    var replyTo: AppMessage?

    @Environment(AppStore.self) private var store

    var body: some View {
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
                Text(r.text).font(.system(size: 12)).foregroundStyle(Theme.muted).lineLimit(1)
                    .padding(.horizontal, 10).padding(.vertical, 4)
                    .overlay(alignment: .leading) { Theme.line.frame(width: 2) }
            }
            if !message.text.isEmpty {
                MessageContent(text: message.text, messageID: message.id, onUser: true)
                    .padding(.horizontal, 14).padding(.vertical, 10)
                    .background(Theme.blue, in: UnevenRoundedRectangle(topLeadingRadius: 18, bottomLeadingRadius: 18, bottomTrailingRadius: 4, topTrailingRadius: 18, style: .continuous))
            }
        }
        .frame(maxWidth: 300, alignment: .trailing)
        .frame(maxWidth: .infinity, alignment: .trailing)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("message.\(message.id)")
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
        .frame(maxWidth: 310, alignment: .leading)
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("message.\(message.id)")
    }

    private var system: some View {
        Text(message.text)
            .font(.system(size: 13))
            .foregroundStyle(Theme.muted)
            .multilineTextAlignment(.center)
            .padding(.horizontal, 24)
            .frame(maxWidth: .infinity)
            .accessibilityIdentifier("message.\(message.id)")
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
                    .accessibilityIdentifier("answer.\(message.id)")
            } else {
                HStack(spacing: 8) {
                    ForEach(Array(message.actions.enumerated()), id: \.offset) { i, label in
                        Button { Task { await store.choose(label, on: message) } } label: {
                            Text(label)
                                .font(.system(size: 14, weight: i == 0 ? .semibold : .medium))
                                .frame(maxWidth: .infinity).frame(height: 44)
                                .background(i == 0 ? Theme.ink : Theme.card, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                                .overlay(RoundedRectangle(cornerRadius: 12, style: .continuous).stroke(i == 0 ? .clear : Theme.line))
                                .foregroundStyle(i == 0 ? .white : Theme.ink)
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("action.\(message.id).\(label)")
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
            .accessibilityIdentifier("thread.typing")
    }
}
