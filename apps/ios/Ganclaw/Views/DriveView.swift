import SwiftUI

struct DriveView: View {
    @Environment(AppStore.self) private var store

    var body: some View {
        let d = store.state.drive
        VStack(spacing: 20) {
            header(d)
            Spacer(minLength: 0)
            VStack(spacing: 28) {
                Waveform(phase: d.phase, muted: d.muted)
                VStack(spacing: 6) {
                    Text(title(d))
                        .font(.system(size: 15, weight: .semibold)).tracking(1.2)
                        .foregroundStyle(d.muted ? Theme.Drive.muted : Theme.amber)
                        .accessibilityIdentifier("drive.phase")
                    Text(subtitle(d)).font(.system(size: 13)).foregroundStyle(Theme.Drive.muted)
                }
                transcript(d)
                if d.detailsSaved {
                    HStack(spacing: 10) {
                        Image(systemName: "bookmark").foregroundStyle(Theme.Drive.muted)
                        Text("Details saved to the thread for when you park.").font(.system(size: 14)).foregroundStyle(Theme.Drive.soft)
                        Spacer(minLength: 0)
                    }
                    .padding(.horizontal, 14).padding(.vertical, 12)
                    .background(Theme.Drive.surface, in: RoundedRectangle(cornerRadius: 14))
                    .overlay(RoundedRectangle(cornerRadius: 14).stroke(Theme.Drive.line))
                    .accessibilityIdentifier("drive.detailsSaved")
                }
                if let e = d.error {
                    Text(e).font(.system(size: 14)).foregroundStyle(Theme.amber).multilineTextAlignment(.center)
                        .accessibilityIdentifier("drive.error")
                }
            }
            Spacer(minLength: 0)
            controls(d)
        }
        .padding(.horizontal, 20)
        .padding(.top, 12)
        .padding(.bottom, 8)
        .background(Theme.Drive.ground.ignoresSafeArea())
        .preferredColorScheme(.dark)
        .accessibilityIdentifier("screen.drive")
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
            .accessibilityIdentifier("drive.agent")
            Spacer()
            Button { store.send(.driveShortAnswers(!d.shortAnswers)) } label: {
                HStack(spacing: 6) {
                    Image(systemName: "text.alignleft").font(.system(size: 13, weight: .semibold))
                    Text(d.shortAnswers ? "Short answers" : "Full answers").font(.system(size: 13, weight: .semibold))
                }
                .padding(.horizontal, 12).frame(height: 32)
                .background(d.shortAnswers ? Theme.Drive.chip : Theme.Drive.surface, in: Capsule())
                .foregroundStyle(d.shortAnswers ? Theme.amber : Theme.Drive.muted)
            }
            .accessibilityAddTraits(d.shortAnswers ? .isSelected : [])
            .accessibilityIdentifier("drive.short")
        }
    }

    private func transcript(_ d: DriveState) -> some View {
        VStack(alignment: .leading, spacing: 14) {
            if !d.heard.isEmpty {
                Text(d.heard).font(.system(size: 22, weight: .medium)).foregroundStyle(Theme.Drive.fg)
                    .accessibilityIdentifier("drive.heard")
            } else {
                if let q = d.lastQuestion {
                    Text("You: \(q)").font(.system(size: 17)).foregroundStyle(Theme.Drive.muted).lineSpacing(3)
                        .accessibilityIdentifier("drive.question")
                }
                if let a = d.lastAnswer {
                    Text(a).font(.system(size: 26, weight: .semibold)).tracking(-0.3).foregroundStyle(Theme.Drive.fg).lineSpacing(4)
                        .accessibilityIdentifier("drive.answer")
                } else if d.phase == .thinking, let agent = d.agent, let s = streamedReply(agent) {
                    Text(s).font(.system(size: 22, weight: .medium)).foregroundStyle(Theme.Drive.soft).lineLimit(4)
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

    private func controls(_ d: DriveState) -> some View {
        HStack {
            roundButton("Repeat", icon: "arrow.counterclockwise", id: "drive.repeat", enabled: d.lastAnswer != nil) { store.repeatLastAnswer() }
            Spacer()
            Button { store.setDriveMuted(!d.muted) } label: {
                VStack(spacing: 6) {
                    Image(systemName: d.muted ? "mic.fill" : "mic.slash").font(.system(size: 36, weight: .medium))
                    Text(d.muted ? "Unmute" : "Mute").font(.system(size: 15, weight: .bold))
                }
                .frame(width: 128, height: 128)
                .background(d.muted ? Theme.amber.opacity(0.18) : Color(hex: 0x1C1D21), in: Circle())
                .overlay(Circle().stroke(d.muted ? Theme.amber : Color(hex: 0x3A3C42), lineWidth: 2))
                .foregroundStyle(d.muted ? Theme.amber : Theme.Drive.fg)
            }
            .accessibilityLabel(d.muted ? "Unmute microphone" : "Mute microphone")
            .accessibilityValue(d.muted ? "muted" : "listening")
            .accessibilityIdentifier("drive.mute")
            Spacer()
            roundButton("Exit", icon: "xmark", id: "drive.exit") { store.exitDrive() }
        }
    }

    private func roundButton(_ title: String, icon: String, id: String, enabled: Bool = true, action: @escaping () -> Void) -> some View {
        Button(action: action) {
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
        .accessibilityIdentifier(id)
    }

    private func title(_ d: DriveState) -> String {
        if d.muted { return "MUTED" }
        let name = Theme.displayName(d.agent ?? "Agent").uppercased()
        switch d.phase {
        case .speaking: return "\(name) IS TALKING"
        case .thinking: return "\(name) IS THINKING"
        case .listening: return d.heard.isEmpty ? "LISTENING" : "HEARING YOU"
        case .idle: return "READY"
        }
    }

    private func subtitle(_ d: DriveState) -> String {
        if d.muted { return "Tap Unmute to talk" }
        return d.phase == .speaking ? "Always listening · just talk to interrupt" : "Always listening · just talk"
    }
}

/// The amber bars; they move while someone is talking.
struct Waveform: View {
    var phase: DrivePhase
    var muted: Bool

    private let base: [CGFloat] = [12, 32, 56, 72, 44, 64, 36, 20, 8]

    var body: some View {
        TimelineView(.animation(minimumInterval: 1 / 20, paused: muted || phase == .idle)) { ctx in
            let t = ctx.date.timeIntervalSinceReferenceDate
            HStack(alignment: .center, spacing: 8) {
                ForEach(base.indices, id: \.self) { i in
                    Capsule()
                        .fill(muted ? Theme.Drive.line : Theme.amber)
                        .frame(width: 8, height: height(i, t))
                }
            }
            .frame(width: 220, height: 220)
            .background(Theme.Drive.surface, in: Circle())
            .background(Circle().fill(Theme.Drive.ring1).padding(-14))
            .background(Circle().fill(Theme.Drive.ring2).padding(-28))
        }
        .padding(28)
        .accessibilityElement()
        .accessibilityLabel(muted ? "Microphone muted" : phase == .speaking ? "Agent is speaking" : "Listening")
    }

    private func height(_ i: Int, _ t: TimeInterval) -> CGFloat {
        let h = base[i]
        switch phase {
        case _ where muted: return 8
        case .speaking: return max(8, h * CGFloat(0.55 + 0.45 * sin(t * 7 + Double(i) * 0.9)))
        case .thinking: return max(8, h * CGFloat(0.3 + 0.15 * sin(t * 3 + Double(i) * 0.6)))
        case .listening: return max(8, h * CGFloat(0.25 + 0.2 * abs(sin(t * 4 + Double(i)))))
        case .idle: return 8
        }
    }
}
