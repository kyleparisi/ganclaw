import SwiftUI

struct DriveView: View {
    @Environment(AppStore.self) private var store

    var body: some View {
        GeometryReader { geo in
            content(compact: geo.size.height < 760)
                .safeAreaInset(edge: .bottom, spacing: 0) {
                    controls(store.state.drive, compact: geo.size.height < 760)
                        .padding(.horizontal, 20)
                        .padding(.top, 12)
                        .padding(.bottom, 8)
                        .background(Theme.Drive.ground)
                }
        }
        .background(Theme.Drive.ground.ignoresSafeArea())
        .preferredColorScheme(.dark)
        .sheet(isPresented: Binding(get: { store.state.drive.transcriptPresented }, set: { store.send(.driveTranscript($0)) })) {
            DriveTranscriptView()
                .environment(store)
                .presentationDetents([.medium, .large])
                .presentationDragIndicator(.visible)
                .preferredColorScheme(.dark)
        }
    }

    private func content(compact: Bool) -> some View {
        let d = store.state.drive
        return VStack(spacing: 12) {
            header(d)
            ScrollView {
                VStack(spacing: compact ? 16 : 24) {
                    Waveform(phase: d.phase, muted: d.muted, size: compact ? 120 : 160)
                    VStack(spacing: 6) {
                        Text(title(d))
                            .font(.system(size: 15, weight: .semibold)).tracking(1.2)
                            .foregroundStyle(d.muted && d.phase == .listening ? Theme.Drive.muted : Theme.amber)
                            .control("drive.phase", label: title(d))
                        Text(subtitle(d)).font(.system(size: 13)).foregroundStyle(Theme.Drive.muted)
                            .multilineTextAlignment(.center)
                    }
                    if !d.heard.isEmpty || d.lastQuestion != nil || d.lastAnswer != nil {
                        CButton("drive.transcript") { store.send(.driveTranscript(true)) } label: {
                            VStack(alignment: .leading, spacing: 12) {
                                transcript(d)
                                HStack {
                                    Text("View conversation")
                                    Spacer()
                                    Image(systemName: "chevron.up")
                                }
                                .font(.system(size: 13, weight: .semibold))
                                .foregroundStyle(Theme.amber)
                            }
                            .padding(16)
                            .background(Theme.Drive.surface, in: RoundedRectangle(cornerRadius: 16))
                            .overlay(RoundedRectangle(cornerRadius: 16).stroke(Theme.Drive.line))
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("View full conversation")
                    }
                    if d.detailsSaved {
                        Text("Details saved to the thread for when you park.")
                            .font(.system(size: 13)).foregroundStyle(Theme.Drive.muted)
                            .control("drive.detailsSaved")
                    }
                    if let e = d.error {
                        Text(e).font(.system(size: 14)).foregroundStyle(Theme.amber).multilineTextAlignment(.center)
                            .control("drive.error")
                    }
                }
                .padding(.vertical, 16)
                .frame(maxWidth: .infinity)
            }
        }
        .padding(.horizontal, 20)
        .padding(.top, 12)
    }

    private func header(_ d: DriveState) -> some View {
        HStack {
            Menu {
                ForEach(store.state.sortedAgents) { a in
                    Button(Theme.displayName(a.name)) {
                        store.send(.driveAgent(a.name))
                        Task { await store.loadThread(a.name) }
                    }
                }
            } label: {
                HStack(spacing: 10) {
                    Text(Theme.initials(d.agent ?? "?"))
                        .font(.system(size: 13, weight: .bold))
                        .foregroundStyle(Theme.Drive.avatarFg)
                        .frame(width: 32, height: 32)
                        .background(Theme.Drive.avatarBg, in: RoundedRectangle(cornerRadius: 10))
                    Text(Theme.displayName(d.agent ?? "Pick an agent")).font(.system(size: 16, weight: .semibold))
                    Image(systemName: "chevron.down").font(.system(size: 13, weight: .semibold))
                }
                .padding(.leading, 8).padding(.trailing, 16).frame(height: 48)
                .background(Theme.Drive.surface, in: Capsule())
                .overlay(Capsule().stroke(Theme.Drive.line))
                .foregroundStyle(Theme.Drive.fg)
            }
            .accessibilityLabel("Agent: \(d.agent ?? "none")")
            .control("drive.agent")
            Spacer()
            CButton("drive.short") { store.send(.driveShortAnswers(!d.shortAnswers)) } label: {
                HStack(spacing: 6) {
                    Image(systemName: "text.alignleft").font(.system(size: 13, weight: .semibold))
                    Text(d.shortAnswers ? "Short answers" : "Full answers").font(.system(size: 13, weight: .semibold))
                }
                .padding(.horizontal, 12).frame(height: 32)
                .background(d.shortAnswers ? Theme.Drive.chip : Theme.Drive.surface, in: Capsule())
                .foregroundStyle(d.shortAnswers ? Theme.amber : Theme.Drive.muted)
            }
            .accessibilityAddTraits(d.shortAnswers ? .isSelected : [])
        }
    }

    private func transcript(_ d: DriveState) -> some View {
        VStack(alignment: .leading, spacing: 14) {
            if !d.heard.isEmpty {
                Text(d.heard).lineLimit(2).font(.system(size: 20, weight: .medium)).foregroundStyle(Theme.Drive.fg)
                    .control("drive.heard")
            } else {
                if let q = d.lastQuestion {
                    Text("You: \(q)").lineLimit(2).font(.system(size: 17)).foregroundStyle(Theme.Drive.muted).lineSpacing(3)
                        .control("drive.question")
                }
                if let a = d.lastAnswer {
                    Text(a).font(.system(size: 20, weight: .semibold)).tracking(-0.3).foregroundStyle(Theme.Drive.fg).lineSpacing(4)
                        .lineLimit(3)
                        .control("drive.answer")
                } else if d.phase == .thinking, let agent = d.agent, let s = streamedReply(agent) {
                    Text(s).font(.system(size: 22, weight: .medium)).foregroundStyle(Theme.Drive.soft).lineLimit(3)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .animation(.easeOut(duration: 0.15), value: d.heard)
    }

    private func streamedReply(_ agent: String) -> String? {
        let t = store.state.thread(agent)
        guard let id = t.streaming.keys.max(), id > store.state.drive.askedAfter, let s = t.streaming[id], !s.isEmpty else { return nil }
        return s.components(separatedBy: "\n---").first
    }

    private func controls(_ d: DriveState, compact: Bool) -> some View {
        let big: CGFloat = compact ? 104 : 128
        return HStack {
            roundButton("Repeat", icon: "arrow.counterclockwise", id: "drive.repeat", enabled: d.lastAnswer != nil) { store.repeatLastAnswer() }
            Spacer()
            CButton("drive.mute") { store.setDriveMuted(!d.muted) } label: {
                VStack(spacing: 6) {
                    Image(systemName: d.muted ? "mic.fill" : "pause.fill").font(.system(size: 36, weight: .medium))
                    Text(d.muted ? "Resume" : "Pause").font(.system(size: 15, weight: .bold))
                }
                .frame(width: big, height: big)
                .background(d.muted ? Theme.amber.opacity(0.18) : Color(hex: 0x1C1D21), in: Circle())
                .overlay(Circle().stroke(d.muted ? Theme.amber : Color(hex: 0x3A3C42), lineWidth: 2))
                .foregroundStyle(d.muted ? Theme.amber : Theme.Drive.fg)
            }
            .accessibilityLabel(d.muted ? "Resume listening" : "Pause listening")
            .accessibilityValue(d.muted ? "paused" : "listening")
            Spacer()
            roundButton("Exit", icon: "xmark", id: "drive.exit") { store.exitDrive() }
        }
    }

    private func roundButton(_ title: String, icon: String, id: String, enabled: Bool = true, action: @escaping () -> Void) -> some View {
        CButton(id, action: action) {
            VStack(spacing: 4) {
                Image(systemName: icon).font(.system(size: 22, weight: .semibold))
                Text(title).font(.system(size: 12, weight: .medium))
            }
            .frame(width: 76, height: 76)
            .background(Theme.Drive.surface, in: Circle())
            .overlay(Circle().stroke(Theme.Drive.line))
            .foregroundStyle(enabled ? Theme.Drive.fg : Theme.Drive.muted)
        }
        .disabled(!enabled)
        .accessibilityLabel(title == "Repeat" ? "Repeat last answer" : "Exit drive mode")
    }

    // Pausing only stops the mic: the agent still answers and is heard, so
    // its speaking and thinking show through a pause.
    private func title(_ d: DriveState) -> String {
        let name = Theme.displayName(d.agent ?? "Agent").uppercased()
        switch d.phase {
        case .speaking: return "\(name) IS TALKING"
        case .thinking: return "\(name) IS THINKING"
        case _ where d.muted: return "PAUSED"
        case .listening: return d.heard.isEmpty ? "LISTENING" : "HEARING YOU"
        case .idle: return "READY"
        }
    }

    private func subtitle(_ d: DriveState) -> String {
        if d.muted { return "Mic paused · tap Resume to talk" }
        guard d.phase == .speaking else { return "Always listening · just talk" }
        #if targetEnvironment(macCatalyst)
        // No echo cancellation on the Mac: the mic would hear the reply, so
        // it waits for the agent to finish.
        return "Your turn when it's done"
        #else
        return "Always listening · just talk to interrupt"
        #endif
    }
}

/// Full text stays in a scrollable sheet instead of growing the drive screen.
struct DriveTranscriptView: View {
    @Environment(AppStore.self) private var store

    var body: some View {
        let d = store.state.drive
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 24) {
                    if !d.heard.isEmpty {
                        section("Hearing you", text: d.heard, id: "drive.transcript.heard")
                    }
                    if let question = d.lastQuestion {
                        section("You", text: question, id: "drive.transcript.question")
                    }
                    if let reply = reply(d) {
                        section(Theme.displayName(d.agent ?? "Agent"), text: reply, id: "drive.transcript.answer")
                    } else if d.phase == .thinking {
                        ProgressView("Waiting for reply…")
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(20)
            }
            .background(Theme.Drive.ground)
            .navigationTitle("Conversation")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    CButton("drive.transcript.done", title: "Done") { store.send(.driveTranscript(false)) }
                        .frame(minWidth: 44, minHeight: 44)
                }
            }
        }
    }

    private func section(_ title: String, text: String, id: String) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title).font(.subheadline.weight(.semibold)).foregroundStyle(Theme.amber)
            Text(text).font(.title3).foregroundStyle(Theme.Drive.fg)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
                .control(id, label: text)
        }
    }

    private func reply(_ d: DriveState) -> String? {
        guard let agent = d.agent else { return d.lastAnswer }
        let thread = store.state.thread(agent)
        if d.lastAnswer != nil {
            return thread.messages.last(where: { $0.id > d.askedAfter && $0.role != .user && $0.status != .streaming })?.text ?? d.lastAnswer
        }
        if let id = thread.streaming.keys.max(), id > d.askedAfter {
            return thread.streaming[id]
        }
        return nil
    }
}

/// The amber bars; they move while someone is talking.
struct Waveform: View {
    var phase: DrivePhase
    var muted: Bool
    var size: CGFloat = 220

    private let base: [CGFloat] = [12, 32, 56, 72, 44, 64, 36, 20, 8]

    var body: some View {
        TimelineView(.animation(minimumInterval: 1 / 20, paused: still)) { ctx in
            let t = ctx.date.timeIntervalSinceReferenceDate
            HStack(alignment: .center, spacing: 8) {
                ForEach(base.indices, id: \.self) { i in
                    Capsule()
                        .fill(still ? Theme.Drive.line : Theme.amber)
                        .frame(width: 8, height: height(i, t))
                }
            }
            .frame(width: size, height: size)
            .background(Theme.Drive.surface, in: Circle())
            .background(Circle().fill(Theme.Drive.ring1).padding(-size * 0.064))
            .background(Circle().fill(Theme.Drive.ring2).padding(-size * 0.127))
        }
        .padding(size * 0.127)
        .accessibilityElement()
        .accessibilityLabel(phase == .speaking ? "Agent is speaking" : phase == .thinking ? "Agent is thinking" : muted ? "Microphone paused" : "Listening")
    }

    /// Nothing is moving: the mic is paused and the agent isn't busy.
    private var still: Bool {
        phase == .idle || (muted && phase == .listening)
    }

    private func height(_ i: Int, _ t: TimeInterval) -> CGFloat {
        let h = base[i] * size / 220
        switch phase {
        case _ where still: return 8
        case .speaking: return max(8, h * CGFloat(0.55 + 0.45 * sin(t * 7 + Double(i) * 0.9)))
        case .thinking: return max(8, h * CGFloat(0.3 + 0.15 * sin(t * 3 + Double(i) * 0.6)))
        case .listening: return max(8, h * CGFloat(0.25 + 0.2 * abs(sin(t * 4 + Double(i)))))
        case .idle: return 8
        }
    }
}
