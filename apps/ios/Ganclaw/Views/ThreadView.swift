import SwiftUI

struct ThreadView: View {
    var agent: String
    @Environment(AppStore.self) private var store
    @Environment(\.dismiss) private var dismiss
    @Environment(\.scenePhase) private var scenePhase
    @State private var unreadBoundary: Int64?
    @State private var capturedBoundary = false
    @State private var atBottom = false
    @State private var nearBottom = false
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
        .task(id: agent) {
            if !capturedBoundary {
                if (store.state.agent(agent)?.unreadCount ?? 0) > 0 {
                    unreadBoundary = store.state.agent(agent)?.lastReadId ?? 0
                }
                capturedBoundary = true
            }
            if !t.loaded { await store.loadThread(agent) }
        }
        // Re-run when the bottom comes into view, the app returns, or the
        // newest message arrives or finishes streaming.
        .task(id: "\(atBottom)-\(scenePhase)-\(t.messages.last?.id ?? 0)-\(t.messages.last?.status.rawValue ?? "")") {
            guard atBottom, scenePhase == .active, let last = t.messages.last else { return }
            do { try await Task.sleep(for: .milliseconds(300)) } catch { return }
            await store.markRead(agent, through: last.id)
        }
        // Starting to type counts as having read the conversation.
        .onChange(of: composerFocused) { _, focused in if focused { markLatestRead(t) } }
    }

    // MARK: Header

    private var header: some View {
        let summary = store.state.agent(agent)
        let st = summary?.state ?? .idle
        return HStack(spacing: 8) {
            CButton("thread.back") { store.send(.pop) } label: {
                Image(systemName: "chevron.left").font(.system(size: 20, weight: .semibold)).frame(width: 44, height: 44)
            }
            .foregroundStyle(Theme.ink)
            .accessibilityLabel("Back to agents")
            AgentAvatar(name: agent, size: 36)
            VStack(alignment: .leading, spacing: 1) {
                Text(Theme.displayName(agent)).font(.system(size: 16, weight: .semibold))
                let status = st.working ? "Working · \((st.detail ?? "working").lowercased())" : statusLine(summary)
                Text(status)
                    .font(.system(size: 12, weight: st.working ? .medium : .regular))
                    .foregroundStyle(st.working ? Theme.blue : Theme.muted)
                    .lineLimit(1)
                    .control("thread.status", label: status)
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
            .control("thread.menu")
            CButton("thread.audio") { Task { await store.startDrive(agent) } } label: {
                HStack(spacing: 6) {
                    Image(systemName: "headphones").foregroundStyle(Theme.amber)
                    Text("Audio").font(.system(size: 13, weight: .semibold))
                }
                .padding(.horizontal, 14).frame(height: 40)
                .background(Theme.ink, in: Capsule())
                .foregroundStyle(.white)
            }
            .accessibilityLabel("Switch to drive mode")
        }
        .padding(.horizontal, 12).padding(.vertical, 8)
        .overlay(alignment: .bottom) { Theme.line.frame(height: 1) }
    }

    private func statusLine(_ s: AgentSummary?) -> String {
        guard let last = s?.lastMessage else { return "Idle" }
        return "Idle · \(last.createdAt.shortAgo())"
    }

    // MARK: Messages

    private func messages(_ t: ThreadState) -> some View {
        ScrollViewReader { proxy in
            GeometryReader { viewport in
                ScrollView {
                    // Not lazy: a lazy stack can't scroll to the newest message
                    // when history arrives after the view appears. Threads load
                    // a page of 100.
                    VStack(spacing: 10) {
                        if t.loading && t.messages.isEmpty {
                            ProgressView().padding(.top, 40)
                        }
                        if let e = t.error, t.messages.isEmpty {
                            Text(e).font(.system(size: 14)).foregroundStyle(Theme.muted).padding(.top, 40)
                        }
                        if t.loaded && t.messages.isEmpty {
                            Text("Say hello to \(Theme.displayName(agent)).").font(.system(size: 15)).foregroundStyle(Theme.muted).padding(.top, 40)
                                .control("thread.empty")
                        }
                        ForEach(Array(t.messages.enumerated()), id: \.element.id) { i, m in
                            if i == 0 || !Calendar.current.isDate(t.messages[i - 1].createdAt, equalTo: m.createdAt, toGranularity: .hour) {
                                Text(dayLabel(m.createdAt)).font(.system(size: 12, weight: .medium)).foregroundStyle(Theme.muted).padding(.top, 4)
                            }
                            if m.id == firstUnread(t) {
                                HStack {
                                    Theme.blue.frame(height: 1)
                                    Text("New messages").font(.system(size: 12, weight: .semibold)).fixedSize()
                                    Theme.blue.frame(height: 1)
                                }
                                .foregroundStyle(Theme.blue)
                                .id("unread")
                                .control("thread.unreadDivider")
                            }
                            MessageRow(message: m, streamed: t.streaming[m.id], replyTo: m.replyTo.flatMap { store.state.quoted($0, in: agent) })
                                .id(m.id)
                        }
                        if store.state.agent(agent)?.state.working == true, t.streaming.isEmpty, t.messages.last?.role == .user {
                            TypingBubble().frame(maxWidth: .infinity, alignment: .leading).id("typing")
                        }
                        Color.clear.frame(height: 4).id("bottom")
                            .background(GeometryReader { geometry in
                                let frame = geometry.frame(in: .named("thread-scroll"))
                                Color.clear.preference(key: ThreadBottomVisible.self,
                                    value: frame.minY >= 0 && frame.maxY <= viewport.size.height)
                                    // Hide the jump control and keep following new content within
                                    // 48 points of the bottom. Read visibility still requires the actual bottom.
                                    .preference(key: ThreadNearBottom.self,
                                        value: frame.minY >= -48 && frame.maxY <= viewport.size.height + 48)
                            })
                    }
                    .padding(.horizontal, 14)
                    .padding(.top, 14)
                }
                .scrollDismissesKeyboard(.interactively)
                .defaultScrollAnchor(.bottom)
                .coordinateSpace(name: "thread-scroll")
                .onPreferenceChange(ThreadBottomVisible.self) { atBottom = $0 }
                .onPreferenceChange(ThreadNearBottom.self) { nearBottom = $0 }
                // Scrolling the conversation counts as reading it.
                .simultaneousGesture(DragGesture(minimumDistance: 12).onEnded { _ in markLatestRead(t) })
                .onAppear { scrollToEntry(proxy, t) }
                .onChange(of: t.loaded) { _, _ in scrollToEntry(proxy, t) }
                .onChange(of: capturedBoundary) { _, _ in scrollToEntry(proxy, t) }
                .onChange(of: t.messages.last?.id) { _, _ in if nearBottom { withAnimation { proxy.scrollTo("bottom", anchor: .bottom) } } }
                .onChange(of: t.streaming) { _, _ in if nearBottom { proxy.scrollTo("bottom", anchor: .bottom) } }
                .overlay(alignment: .bottom) {
                    if !nearBottom && t.loaded && !t.messages.isEmpty {
                        CButton("thread.latest", title: "Jump to latest") {
                            withAnimation { proxy.scrollTo("bottom", anchor: .bottom) }
                        }
                        .font(.system(size: 13, weight: .semibold))
                        .padding(10).background(Theme.card, in: Capsule())
                        .padding(.bottom, 8)
                    }
                }
                .control("thread.messages", scroll: { to in
                    switch to {
                    case .top: if let first = t.messages.first { proxy.scrollTo(first.id, anchor: .top) }
                    case .bottom: proxy.scrollTo("bottom", anchor: .bottom)
                    }
                    markLatestRead(t) // what the drag gesture does
                })
            }
        }
    }

    /// Marks everything loaded as read: the user is engaging with the thread.
    private func markLatestRead(_ t: ThreadState) {
        guard scenePhase == .active, let last = t.messages.last else { return }
        Task { await store.markRead(agent, through: last.id) }
    }

    private func firstUnread(_ t: ThreadState) -> Int64? {
        guard let boundary = unreadBoundary else { return nil }
        return t.messages.first { $0.id > boundary && $0.role != .user && $0.status != .streaming }?.id
    }

    private func scrollToEntry(_ proxy: ScrollViewProxy, _ t: ThreadState) {
        DispatchQueue.main.async {
            if firstUnread(t) != nil { proxy.scrollTo("unread", anchor: .top) }
            else { proxy.scrollTo("bottom", anchor: .bottom) }
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
                Text(store.state.quoted(id, in: agent)?.text ?? "a message").font(.system(size: 13)).foregroundStyle(Theme.text2).lineLimit(1)
            }
            Spacer()
            CButton("thread.cancelReply") { store.send(.replyTo(agent, nil)) } label: { Image(systemName: "xmark").frame(width: 36, height: 36) }
                .foregroundStyle(Theme.text2)
                .accessibilityLabel("Cancel reply")
        }
        .frame(height: 44)
        .padding(.horizontal, 14)
        .background(Theme.card)
        .control("thread.replyBar")
    }

    private func composer(_ t: ThreadState) -> some View {
        HStack(alignment: .bottom, spacing: 8) {
            CButton("composer.attach") { store.send(.attachOpen(agent)) } label: {
                Image(systemName: "plus").font(.system(size: 18, weight: .semibold))
                    .frame(width: 44, height: 44)
                    .background(Theme.card, in: Circle())
                    .overlay(Circle().stroke(Theme.line))
                    .foregroundStyle(Theme.ink)
            }
            .accessibilityLabel("Add attachment")

            HStack(alignment: .bottom, spacing: 4) {
                if recorder.recording {
                    HStack(spacing: 8) {
                        Circle().fill(Theme.red).frame(width: 8, height: 8)
                        Text("Recording \(recorder.elapsedText)").font(.system(size: 15, design: .monospaced)).foregroundStyle(Theme.ink)
                        Spacer()
                        CButton("composer.cancelRecording", title: "Cancel") { recorder.cancel() }.font(.system(size: 14, weight: .medium))
                    }
                    .padding(.leading, 16).frame(minHeight: 42)
                } else {
                    TextField("Message \(Theme.displayName(agent))", text: Binding(get: { store.state.thread(agent).draft }, set: { store.send(.draft(agent, $0)) }), axis: .vertical)
                        .font(.system(size: 15))
                        .lineLimit(1...6)
                        .padding(.leading, 16).padding(.vertical, 11)
                        .focused($composerFocused)
                        .accessibilityLabel("Message")
                        // /tap focuses the field, as a finger would.
                        .control("composer.text", action: { composerFocused = true })
                }
                CButton("composer.mic") { toggleRecording() } label: {
                    Image(systemName: recorder.recording ? "stop.fill" : "mic")
                        .font(.system(size: 18))
                        .frame(width: 36, height: 42)
                        .foregroundStyle(recorder.recording ? Theme.red : Theme.text2)
                }
                .accessibilityLabel(recorder.recording ? "Stop and send voice note" : "Record voice note")
                .padding(.trailing, 4)
            }
            .background(Theme.card, in: RoundedRectangle(cornerRadius: 22, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 22, style: .continuous).stroke(Theme.line))

            CButton("composer.send") { Task { await store.sendDraft(agent) } } label: {
                Image(systemName: "arrow.up").font(.system(size: 18, weight: .bold))
                    .frame(width: 44, height: 44)
                    .background(canSend(t) ? Theme.blue : Theme.blue.opacity(0.4), in: Circle())
                    .foregroundStyle(.white)
            }
            .disabled(!canSend(t))
            .keyboardShortcut(.return, modifiers: .command)
            .accessibilityLabel("Send")
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
                do {
                    try await recorder.start(store.platform.recorder)
                } catch {
                    store.send(.banner(store.describe(error)))
                }
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
        .control("message.\(message.id)")
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
        .control("message.\(message.id)")
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

private struct ThreadBottomVisible: PreferenceKey {
    static var defaultValue = false
    static func reduce(value: inout Bool, nextValue: () -> Bool) { value = value || nextValue() }
}

private struct ThreadNearBottom: PreferenceKey {
    static var defaultValue = false
    static func reduce(value: inout Bool, nextValue: () -> Bool) { value = value || nextValue() }
}
