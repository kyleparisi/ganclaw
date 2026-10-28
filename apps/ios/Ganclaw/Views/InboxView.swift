import SwiftUI

struct InboxView: View {
    @Environment(AppStore.self) private var store

    var body: some View {
        let s = store.state
        ZStack(alignment: .bottom) {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    header
                    FilterBar(selected: s.filter) { store.send(.setFilter($0)) }
                    if s.connection == .disconnected {
                        Label("Reconnecting…", systemImage: "wifi.exclamationmark")
                            .font(.system(size: 13, weight: .medium)).foregroundStyle(Theme.muted)
                            .control("inbox.reconnecting")
                    }
                    if s.filter == .tasks {
                        TaskList()
                    } else if s.filter == .jobs {
                        JobList(jobs: s.jobs)
                    } else {
                        rows(s)
                    }
                }
                .padding(.horizontal, 20)
                .padding(.top, 8)
                .padding(.bottom, 120)
            }
            .refreshable { await store.refresh() }

            bottomBar
        }
        .background(Theme.ground.ignoresSafeArea())
        .toolbar(.hidden, for: .navigationBar)
    }

    private var header: some View {
        HStack {
            Text("Agents").font(.system(size: 30, weight: .bold)).tracking(-0.6)
                .accessibilityAddTraits(.isHeader)
            Spacer()
            CButton("inbox.settings") { store.send(.push(.settings)) } label: {
                Image(systemName: "gearshape")
                    .font(.system(size: 18, weight: .semibold))
                    .foregroundStyle(Theme.ink)
                    .frame(width: 44, height: 44)
                    .background(Theme.card, in: Circle())
                    .overlay(Circle().stroke(Theme.line))
            }
            .accessibilityLabel("Settings")
        }
        .padding(.top, 8)
    }

    @ViewBuilder
    private func rows(_ s: AppState) -> some View {
        let rows = s.inboxRows
        VStack(spacing: 8) {
            if !s.agentsLoaded {
                ProgressView().frame(maxWidth: .infinity).padding(.top, 40)
            } else if rows.isEmpty {
                Text(s.filter == .running ? "No agent is working right now." : s.filter == .needsYou ? "Nothing needs you." : "No agents configured.")
                    .font(.system(size: 15)).foregroundStyle(Theme.muted)
                    .frame(maxWidth: .infinity).padding(.top, 40)
                    .control("inbox.empty")
            }
            ForEach(rows) { a in
                CButton("inbox.row.\(a.name)") { store.openThread(a.name) } label: { AgentRow(agent: a, streaming: s.thread(a.name).streaming) }
                    .buttonStyle(.plain)
            }
            if s.filter == .all, !s.jobs.isEmpty {
                CButton("inbox.jobs") { store.send(.push(.jobs)) } label: { JobsSummaryRow(jobs: s.jobs) }
                    .buttonStyle(.plain)
                    .padding(.top, 4)
            }
        }
    }

    private var bottomBar: some View {
        HStack(spacing: 10) {
            CButton("inbox.drive") { Task { await store.startDrive(nil) } } label: {
                HStack(spacing: 10) {
                    Image(systemName: "car.fill").foregroundStyle(Theme.amber)
                    Text("Drive mode").font(.system(size: 16, weight: .semibold))
                }
                .frame(maxWidth: .infinity).frame(height: 60)
                .background(Theme.ink, in: Capsule())
                .foregroundStyle(.white)
                .shadow(color: Theme.ink.opacity(0.22), radius: 12, y: 8)
            }
            CButton("inbox.talk") { Task { await store.startDrive(store.state.sortedAgents.first?.name) } } label: {
                Image(systemName: "mic").font(.system(size: 20, weight: .medium))
                    .frame(width: 60, height: 60)
                    .background(Theme.card, in: Circle())
                    .overlay(Circle().stroke(Theme.line))
                    .foregroundStyle(Theme.ink)
            }
            .accessibilityLabel("Talk to an agent")
        }
        .padding(.horizontal, 20)
        .padding(.bottom, 12)
    }
}

struct FilterBar: View {
    var selected: InboxFilter
    var select: (InboxFilter) -> Void

    var body: some View {
        HStack(spacing: 6) {
            ForEach(InboxFilter.allCases) { f in
                CButton("filter.\(f.rawValue)") { select(f) } label: {
                    Text(f.title)
                        .font(.system(size: 13, weight: f == selected ? .semibold : .medium))
                        .foregroundStyle(f == selected ? Theme.ink : Color(hex: 0x55565B))
                        .frame(maxWidth: .infinity).frame(height: 36)
                        .background(f == selected ? Theme.card : .clear, in: RoundedRectangle(cornerRadius: 9, style: .continuous))
                        .shadow(color: .black.opacity(f == selected ? 0.06 : 0), radius: 1, y: 1)
                }
                .buttonStyle(.plain)
                .accessibilityAddTraits(f == selected ? .isSelected : [])
            }
        }
        .padding(4)
        .background(Theme.segment, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
    }
}

struct AgentRow: View {
    var agent: AgentSummary
    var streaming: [Int64: String] = [:]

    private var idle: Bool {
        guard !agent.state.working, !agent.needsYou else { return false }
        guard let last = agent.lastMessage else { return true }
        return Date().timeIntervalSince(last.createdAt) > 86400
    }

    var body: some View {
        HStack(alignment: idle ? .center : .top, spacing: 12) {
            AgentAvatar(name: agent.name, dim: idle)
            VStack(alignment: .leading, spacing: 4) {
                HStack(alignment: .firstTextBaseline) {
                    Text(Theme.displayName(agent.name))
                        .font(.system(size: 16, weight: .semibold))
                        .foregroundStyle(idle ? Theme.text2 : Theme.ink)
                    Spacer()
                    if let unread = agent.unreadCount, unread > 0 {
                        Text(unread > 99 ? "99+" : String(unread))
                            .font(.system(size: 12, weight: .bold))
                            .foregroundStyle(.white)
                            .padding(.horizontal, 7).padding(.vertical, 3)
                            .background(Theme.blue, in: Capsule())
                            .accessibilityLabel("\(unread) unread messages")
                            .control("inbox.unread.\(agent.name)")
                    }
                    if agent.state.working {
                        Text("Running").font(.system(size: 12, weight: .semibold)).foregroundStyle(Theme.blue)
                    } else if let last = agent.lastMessage, !idle {
                        Text(last.createdAt.shortAgo()).font(.system(size: 12)).foregroundStyle(Theme.muted)
                    }
                }
                preview
                    .font(.system(size: 14))
                    .foregroundStyle(idle ? Theme.muted : Theme.text2)
                    .lineLimit(1)
                if !idle { chips }
            }
        }
        .padding(14)
        .background(idle ? Color.clear : Theme.card, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay {
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .stroke(idle ? Color(hex: 0xD6D3CB) : Theme.line, style: StrokeStyle(lineWidth: 1, dash: idle ? [5, 4] : []))
        }
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }

    @ViewBuilder
    private var preview: some View {
        if agent.state.working {
            if let text = streaming.values.first, !text.isEmpty {
                Text(text.replacingOccurrences(of: "\n", with: " "))
            } else {
                Text(agent.state.detail ?? "Working")
            }
        } else if let last = agent.lastMessage {
            if !last.files.isEmpty && last.text.isEmpty {
                Label(last.files.count == 1 ? "Sent \(last.files[0].name)" : "Sent \(last.files.count) files", systemImage: "paperclip")
            } else {
                Text((last.role == .user ? "You: " : "") + last.text.replacingOccurrences(of: "\n", with: " "))
            }
        } else {
            Text("No messages yet")
        }
    }

    @ViewBuilder
    private var chips: some View {
        let last = agent.lastMessage
        let action = agent.needsYou ? last?.pendingActions.first : nil
        let file = last?.files.first
        if action != nil || file != nil || last?.status == .error {
            HStack(spacing: 6) {
                if let action {
                    chip(action, fg: Theme.amberText, bg: Theme.amberChip, bold: true)
                }
                if last?.status == .error {
                    chip("Failed", fg: Theme.red, bg: Theme.redSoft, bold: true)
                }
                if let file {
                    chip(file.name, fg: Theme.text2, bg: Theme.chip, mono: true)
                }
            }
            .padding(.top, 4)
        }
    }

    private func chip(_ text: String, fg: Color, bg: Color, bold: Bool = false, mono: Bool = false) -> some View {
        Text(text)
            .font(.system(size: 12, weight: bold ? .semibold : .regular, design: mono ? .monospaced : .default))
            .foregroundStyle(fg)
            .lineLimit(1)
            .padding(.horizontal, 8).padding(.vertical, 3)
            .background(bg, in: RoundedRectangle(cornerRadius: 6))
    }
}

struct JobsSummaryRow: View {
    var jobs: [JobSummary]

    var body: some View {
        let problems = jobs.filter { $0.problems > 0 }.count
        let quiet = jobs.count - problems
        HStack(spacing: 12) {
            Image(systemName: "clock").font(.system(size: 18)).foregroundStyle(Theme.text2)
            VStack(alignment: .leading, spacing: 1) {
                Text("Scheduled · \(jobs.count) \(jobs.count == 1 ? "job" : "jobs")").font(.system(size: 15, weight: .semibold))
                Text(problems == 0 ? "All ran quietly" : "\(quiet) ran quietly · \(problems) \(problems == 1 ? "warning" : "warnings")")
                    .font(.system(size: 13)).foregroundStyle(Theme.text2)
            }
            Spacer()
            if problems > 0 {
                Text("\(problems)").font(.system(size: 12, weight: .bold)).foregroundStyle(Theme.amberText)
                    .padding(.horizontal, 6).frame(minWidth: 22, minHeight: 22)
                    .background(Theme.amberChip, in: Capsule())
            }
            Image(systemName: "chevron.right").font(.system(size: 14, weight: .semibold)).foregroundStyle(Theme.text2)
        }
        .padding(.horizontal, 14)
        .frame(minHeight: 56)
        .background(Theme.segment, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .foregroundStyle(Theme.ink)
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }
}
