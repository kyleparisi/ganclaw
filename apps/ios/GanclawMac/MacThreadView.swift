import SwiftUI
import UniformTypeIdentifiers

/// A conversation in the detail column. Same reading rules as iOS: the
/// thread is marked read when its bottom is on screen while the window is
/// active, or when the user starts typing or scrolls.
struct MacThreadView: View {
    var agent: String
    @Environment(AppStore.self) private var store
    @Environment(\.scenePhase) private var scenePhase
    @State private var unreadBoundary: Int64?
    @State private var capturedBoundary = false
    @State private var atBottom = false
    @State private var nearBottom = false
    @State private var recorder = VoiceRecorder()
    @State private var composerFocused = false
    @State private var focusRequest = 0
    @State private var dropTargeted = false

    /// Readable line length for the conversation column.
    private let columnWidth: CGFloat = 820

    var body: some View {
        let t = store.state.thread(agent)
        let summary = store.state.agent(agent)
        VStack(spacing: 0) {
            messages(t)
            if let rid = t.replyTo { replyBar(rid) }
            composer(t)
        }
        .background(Theme.ground)
        .navigationTitle(Theme.displayName(agent))
        .navigationSubtitle(statusLine(summary))
        .toolbar { toolbar(summary) }
        .overlay {
            if dropTargeted {
                RoundedRectangle(cornerRadius: 14, style: .continuous)
                    .stroke(Theme.blue, style: StrokeStyle(lineWidth: 2, dash: [6, 4]))
                    .background(Theme.blueSoft.opacity(0.35), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                    .overlay(Label("Drop to attach", systemImage: "paperclip").font(.headline).foregroundStyle(Theme.blue))
                    .padding(12)
                    .allowsHitTesting(false)
            }
        }
        .dropDestination(for: URL.self) { urls, _ in
            attach(urls)
            return !urls.isEmpty
        } isTargeted: { dropTargeted = $0 }
        .task(id: agent) {
            if !capturedBoundary {
                if (store.state.agent(agent)?.unreadCount ?? 0) > 0 {
                    unreadBoundary = store.state.agent(agent)?.lastReadId ?? 0
                }
                capturedBoundary = true
            }
            focusRequest += 1
            if !t.loaded { await store.loadThread(agent) }
        }
        .task(id: "\(atBottom)-\(scenePhase)-\(t.messages.last?.id ?? 0)-\(t.messages.last?.status.rawValue ?? "")") {
            guard atBottom, scenePhase == .active, let last = t.messages.last else { return }
            do { try await Task.sleep(for: .milliseconds(300)) } catch { return }
            await store.markRead(agent, through: last.id)
        }
        .onChange(of: composerFocused) { _, focused in if focused { markLatestRead(t) } }
    }

    // MARK: Toolbar

    @ToolbarContentBuilder
    private func toolbar(_ summary: AgentSummary?) -> some ToolbarContent {
        let working = summary?.state.working ?? false
        ToolbarItemGroup {
            if working {
                Button { Task { await store.stop(agent) } } label: { Label("Stop", systemImage: "stop.circle") }
                    .help("Stop the current turn (⌘.)")
            }
            Button { store.send(.attachOpen(agent)) } label: { Label("Attach", systemImage: "paperclip") }
                .help("Attach files (⇧⌘A)")
            Button { Task { await store.startDrive(agent) } } label: { Label("Drive mode", systemImage: "headphones") }
                .help("Talk to \(Theme.displayName(agent)) hands-free (⇧⌘D)")
            Menu {
                Button("New Conversation", systemImage: "arrow.counterclockwise") { Task { await store.reset(agent) } }
            } label: { Label("More", systemImage: "ellipsis.circle") }
        }
    }

    private func statusLine(_ s: AgentSummary?) -> String {
        if let st = s?.state, st.working { return "Working · \((st.detail ?? "working").lowercased())" }
        guard let last = s?.lastMessage else { return "Idle" }
        return "Idle · \(last.createdAt.shortAgo())"
    }

    // MARK: Messages

    private func messages(_ t: ThreadState) -> some View {
        ScrollViewReader { proxy in
            GeometryReader { viewport in
                ScrollView {
                    VStack(spacing: 10) {
                        if t.loading && t.messages.isEmpty {
                            ProgressView().padding(.top, 40)
                        }
                        if let e = t.error, t.messages.isEmpty {
                            Text(e).foregroundStyle(Theme.muted).padding(.top, 40)
                        }
                        if t.loaded && t.messages.isEmpty {
                            Text("Say hello to \(Theme.displayName(agent)).").foregroundStyle(Theme.muted).padding(.top, 40)
                        }
                        ForEach(Array(t.messages.enumerated()), id: \.element.id) { i, m in
                            if i == 0 || !Calendar.current.isDate(t.messages[i - 1].createdAt, equalTo: m.createdAt, toGranularity: .hour) {
                                Text(dayLabel(m.createdAt)).font(.system(size: 11, weight: .medium)).foregroundStyle(Theme.muted).padding(.top, 6)
                            }
                            if m.id == firstUnread(t) {
                                HStack {
                                    Theme.blue.frame(height: 1)
                                    Text("New messages").font(.system(size: 11, weight: .semibold)).fixedSize()
                                    Theme.blue.frame(height: 1)
                                }
                                .foregroundStyle(Theme.blue)
                                .id("unread")
                            }
                            HoverReply(enabled: m.role != .system && m.status != .streaming, alignLeading: m.role == .user) {
                                reply(to: m)
                            } content: {
                                MessageRow(message: m, streamed: t.streaming[m.id],
                                           replyTo: m.replyTo.flatMap { store.state.quoted($0, in: agent) },
                                           bubbleWidth: 560) { reply(to: m) }
                            }
                            .id(m.id)
                        }
                        if store.state.agent(agent)?.state.working == true, t.streaming.isEmpty, t.messages.last?.role == .user {
                            TypingBubble().frame(maxWidth: .infinity, alignment: .leading).id("typing")
                        }
                        Color.clear.frame(height: 4).id("bottom")
                            .background(GeometryReader { g in
                                let f = g.frame(in: .named("mac-thread"))
                                Color.clear
                                    .preference(key: MacBottomVisible.self, value: f.minY >= 0 && f.maxY <= viewport.size.height)
                                    .preference(key: MacNearBottom.self, value: f.minY >= -48 && f.maxY <= viewport.size.height + 48)
                            })
                    }
                    .frame(maxWidth: columnWidth)
                    .padding(.horizontal, 20)
                    .padding(.top, 16)
                    .frame(maxWidth: .infinity)
                }
                .defaultScrollAnchor(.bottom)
                .coordinateSpace(name: "mac-thread")
                .onPreferenceChange(MacBottomVisible.self) { atBottom = $0 }
                .onPreferenceChange(MacNearBottom.self) { nearBottom = $0 }
                .onAppear { scrollToEntry(proxy, t) }
                .onChange(of: t.loaded) { _, _ in scrollToEntry(proxy, t) }
                .onChange(of: t.messages.last?.id) { _, _ in if nearBottom { withAnimation { proxy.scrollTo("bottom", anchor: .bottom) } } }
                .onChange(of: t.streaming) { _, _ in if nearBottom { proxy.scrollTo("bottom", anchor: .bottom) } }
                .overlay(alignment: .bottom) {
                    if !nearBottom && t.loaded && !t.messages.isEmpty {
                        Button { withAnimation { proxy.scrollTo("bottom", anchor: .bottom) } } label: {
                            Label("Jump to latest", systemImage: "arrow.down")
                                .font(.system(size: 12, weight: .semibold))
                                .padding(.horizontal, 12).padding(.vertical, 7)
                                .background(Theme.card, in: Capsule())
                                .overlay(Capsule().stroke(Theme.line))
                        }
                        .buttonStyle(.plain)
                        .padding(.bottom, 10)
                    }
                }
            }
        }
    }

    private func reply(to m: AppMessage) {
        store.send(.replyTo(agent, m.id))
        focusRequest += 1
    }

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

    private func replyBar(_ id: Int64) -> some View {
        HStack(spacing: 8) {
            Theme.blue.frame(width: 3).clipShape(Capsule())
            VStack(alignment: .leading, spacing: 1) {
                Text("Replying to").font(.system(size: 11, weight: .semibold)).foregroundStyle(Theme.blue)
                Text(store.state.quoted(id, in: agent)?.replyPreview ?? "a message").font(.system(size: 12)).foregroundStyle(Theme.text2).lineLimit(1)
            }
            Spacer()
            Button { store.send(.replyTo(agent, nil)) } label: { Image(systemName: "xmark") }
                .buttonStyle(.borderless)
                .keyboardShortcut(.cancelAction)
                .help("Cancel reply (Esc)")
        }
        .frame(maxWidth: columnWidth)
        .frame(height: 40)
        .padding(.horizontal, 20)
        .frame(maxWidth: .infinity)
        .background(Theme.card)
        .overlay(alignment: .top) { Theme.line.frame(height: 1) }
    }

    private func composer(_ t: ThreadState) -> some View {
        HStack(alignment: .bottom, spacing: 8) {
            Button { store.send(.attachOpen(agent)) } label: {
                Image(systemName: "plus").font(.system(size: 14, weight: .semibold)).frame(width: 30, height: 30)
            }
            .buttonStyle(.borderless)
            .help("Attach files (⇧⌘A)")
            .padding(.bottom, 3)

            HStack(alignment: .bottom, spacing: 4) {
                if recorder.recording {
                    HStack(spacing: 8) {
                        Circle().fill(Theme.red).frame(width: 8, height: 8)
                        Text("Recording \(recorder.elapsedText)").font(.system(size: 13, design: .monospaced))
                        Spacer()
                        Button("Cancel") { recorder.cancel() }.buttonStyle(.borderless)
                    }
                    .frame(minHeight: 20)
                } else {
                    MacComposerField(
                        text: Binding(get: { store.state.thread(agent).draft }, set: { store.send(.draft(agent, $0)) }),
                        focused: $composerFocused,
                        focusRequest: focusRequest,
                        placeholder: "Message \(Theme.displayName(agent))",
                        pasteboard: store.platform.pasteboard,
                        onSubmit: { Task { await store.sendDraft(agent) } },
                        onImage: { image in
                            store.send(.attachOpen(agent))
                            store.addAttachment(name: image.name, mime: image.mime, data: image.data)
                        }
                    )
                    .overlay(alignment: .topLeading) {
                        if t.draft.isEmpty {
                            Text("Message \(Theme.displayName(agent))")
                                .font(.system(size: 14))
                                .foregroundStyle(.tertiary)
                                .allowsHitTesting(false)
                                .accessibilityHidden(true)
                        }
                    }
                }
                Button { toggleRecording() } label: {
                    Image(systemName: recorder.recording ? "stop.fill" : "mic")
                        .foregroundStyle(recorder.recording ? Theme.red : Theme.text2)
                        .frame(width: 22, height: 20)
                }
                .buttonStyle(.borderless)
                .help(recorder.recording ? "Stop and send voice note" : "Record a voice note")
            }
            .padding(.horizontal, 12).padding(.vertical, 8)
            .background(Theme.card, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 12, style: .continuous).stroke(composerFocused ? Theme.blue.opacity(0.6) : Theme.line))

            Button { Task { await store.sendDraft(agent) } } label: {
                Image(systemName: "arrow.up").font(.system(size: 13, weight: .bold))
                    .frame(width: 30, height: 30)
                    .background(canSend(t) ? Theme.blue : Theme.blue.opacity(0.35), in: Circle())
                    .foregroundStyle(.white)
            }
            .buttonStyle(.plain)
            .disabled(!canSend(t))
            .help("Send (Return)")
            .padding(.bottom, 3)
        }
        .frame(maxWidth: columnWidth)
        .padding(.horizontal, 20).padding(.vertical, 12)
        .frame(maxWidth: .infinity)
        .background(Theme.ground)
        .overlay(alignment: .top) { Theme.line.frame(height: 1) }
    }

    private func canSend(_ t: ThreadState) -> Bool {
        !t.sending && !t.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    private func attach(_ urls: [URL]) {
        let files = urls.filter(\.isFileURL)
        guard !files.isEmpty else { return }
        store.send(.attachOpen(agent))
        MacAttachSheet.add(files, to: store)
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
                do { try await recorder.start(store.platform.recorder) }
                catch { store.send(.banner(store.describe(error))) }
            }
        }
    }
}

/// Shows a reply button beside a message while the pointer is over it.
private struct HoverReply<Content: View>: View {
    var enabled: Bool
    var alignLeading: Bool
    var action: () -> Void
    @ViewBuilder var content: Content
    @State private var hovering = false

    var body: some View {
        content
            .overlay(alignment: alignLeading ? .topLeading : .topTrailing) {
                if enabled && hovering {
                    Button(action: action) {
                        Image(systemName: "arrowshape.turn.up.left")
                            .font(.system(size: 11, weight: .semibold))
                            .frame(width: 24, height: 24)
                            .background(Theme.card, in: Circle())
                            .overlay(Circle().stroke(Theme.line))
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(Theme.text2)
                    .help("Reply")
                    .padding(4)
                }
            }
            .onHover { hovering = $0 }
    }
}

private struct MacBottomVisible: PreferenceKey {
    static var defaultValue = false
    static func reduce(value: inout Bool, nextValue: () -> Bool) { value = value || nextValue() }
}

private struct MacNearBottom: PreferenceKey {
    static var defaultValue = false
    static func reduce(value: inout Bool, nextValue: () -> Bool) { value = value || nextValue() }
}
