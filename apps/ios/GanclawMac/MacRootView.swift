import SwiftUI

struct MacRootView: View {
    @Environment(AppStore.self) private var store
    @State private var search = ""

    var body: some View {
        let s = store.state
        Group {
            if s.connection == .unpaired {
                MacPairView()
            } else {
                NavigationSplitView {
                    sidebar(s)
                        .navigationSplitViewColumnWidth(min: 240, ideal: 290, max: 380)
                } detail: {
                    detail(s)
                }
            }
        }
        .preferredColorScheme(.light)
        .overlay(alignment: .top) {
            if let b = s.banner, s.connection != .unpaired {
                BannerView(text: b) { store.send(.banner(nil)) }
                    .frame(maxWidth: 560)
                    .padding(.top, 8)
                    .transition(.move(edge: .top).combined(with: .opacity))
            }
        }
        .overlay(alignment: .bottom) {
            if let t = s.toast {
                ToastView(text: t)
                    .padding(.bottom, 90)
                    .transition(.opacity.combined(with: .scale(scale: 0.95)))
            }
        }
        .animation(.easeOut(duration: 0.2), value: s.toast)
        .animation(.easeOut(duration: 0.2), value: s.banner)
        .sheet(isPresented: Binding(get: { store.state.drive.active }, set: { if !$0 { store.exitDrive() } })) {
            // Presented views don't inherit the environment on the Mac.
            DriveView()
                .environment(store)
                .frame(width: 440, height: 720)
        }
        .sheet(isPresented: Binding(get: { store.state.attach.presented }, set: { if !$0 { store.send(.attachClose) } })) {
            MacAttachSheet()
                .environment(store)
        }
    }

    // MARK: Sidebar

    /// The sidebar row that owns the open page: a job's runs belong to
    /// Scheduled jobs.
    private var selection: Binding<Route?> {
        Binding(get: {
            switch store.state.path.last {
            case .job?: return .jobs
            case let r: return r
            }
        }, set: { r in
            if let r { store.select(r) }
        })
    }

    private func sidebar(_ s: AppState) -> some View {
        let query = search.trimmingCharacters(in: .whitespaces).lowercased()
        let agents = s.sortedAgents.filter {
            query.isEmpty || $0.name.lowercased().contains(query) || Theme.displayName($0.name).lowercased().contains(query)
        }
        return List(selection: selection) {
            Section {
                Label("Open items", systemImage: "checklist")
                    .badge(s.agents.filter(\.needsYou).count)
                    .tag(Route.openItems)
                Label("Scheduled jobs", systemImage: "clock")
                    .badge(s.jobProblems)
                    .tag(Route.jobs)
            }
            Section("Agents") {
                if !s.agentsLoaded {
                    ProgressView().controlSize(.small).frame(maxWidth: .infinity)
                } else if agents.isEmpty {
                    Text(query.isEmpty ? "No agents configured." : "No matches.")
                        .foregroundStyle(.secondary)
                }
                ForEach(agents) { a in
                    SidebarAgentRow(agent: a, streaming: s.thread(a.name).streaming)
                        .tag(Route.thread(a.name))
                        .contextMenu {
                            Button("Drive Mode") { Task { await store.startDrive(a.name) } }
                            if a.state.working {
                                Button("Stop") { Task { await store.stop(a.name) } }
                            }
                            Button("New Conversation") { Task { await store.reset(a.name) } }
                        }
                }
            }
        }
        .listStyle(.sidebar)
        .searchable(text: $search, placement: .sidebar, prompt: "Agents")
        .safeAreaInset(edge: .bottom) { connectionFooter(s) }
        .toolbar {
            ToolbarItem {
                Button { Task { await store.startDrive(store.currentAgent) } } label: {
                    Label("Drive mode", systemImage: "car.fill")
                }
                .help("Talk to an agent hands-free (⇧⌘D)")
                .disabled(s.agents.isEmpty)
            }
        }
    }

    private func connectionFooter(_ s: AppState) -> some View {
        HStack(spacing: 6) {
            Circle()
                .fill(s.connection == .connected ? Theme.teal : s.connection == .disconnected ? Theme.red : Theme.amber)
                .frame(width: 7, height: 7)
            Text(s.connection == .connected ? (s.server?.host ?? "Connected") : s.connection == .disconnected ? "Reconnecting…" : "Connecting…")
                .lineLimit(1)
            Spacer()
            SettingsLink { Image(systemName: "gearshape") }
                .buttonStyle(.borderless)
                .help("Settings (⌘,)")
        }
        .font(.system(size: 12))
        .foregroundStyle(.secondary)
        .padding(.horizontal, 14).padding(.vertical, 8)
    }

    // MARK: Detail

    @ViewBuilder
    private func detail(_ s: AppState) -> some View {
        switch s.path.last {
        case .thread(let a)?:
            MacThreadView(agent: a).id(a)
        case .jobs?:
            ScrollView { JobList(jobs: s.jobs).padding(20).frame(maxWidth: 760) }
                .frame(maxWidth: .infinity)
                .background(Theme.ground)
                .navigationTitle("Scheduled jobs")
                .navigationSubtitle("\(s.jobs.count) \(s.jobs.count == 1 ? "job" : "jobs")")
        case .job(let j)?:
            JobView(job: j)
                .navigationTitle(jobTitle(j))
        case .openItems?:
            ScrollView { OpenItemsView().padding(20).frame(maxWidth: 760) }
                .frame(maxWidth: .infinity)
                .background(Theme.ground)
                .navigationTitle("Open items")
        default:
            ContentUnavailableView {
                Label("No conversation selected", systemImage: "bubble.left.and.bubble.right")
            } description: {
                Text(s.agents.isEmpty ? "Agents appear here once the server has some." : "Pick an agent in the sidebar, or press ⌘1.")
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(Theme.ground)
        }
    }
}

/// A compact agent row for the sidebar: avatar, name, preview, and what
/// needs attention (unread count, running, a decision waiting).
struct SidebarAgentRow: View {
    var agent: AgentSummary
    var streaming: [Int64: String] = [:]

    var body: some View {
        HStack(alignment: .center, spacing: 10) {
            AgentAvatar(name: agent.name, size: 30)
                .overlay(alignment: .bottomTrailing) {
                    if agent.state.working {
                        Circle().fill(Theme.blue).frame(width: 9, height: 9)
                            .overlay(Circle().stroke(.background, lineWidth: 2))
                            .offset(x: 2, y: 2)
                    }
                }
            VStack(alignment: .leading, spacing: 2) {
                HStack(alignment: .firstTextBaseline) {
                    Text(Theme.displayName(agent.name))
                        .font(.system(size: 13, weight: .semibold))
                        .lineLimit(1)
                    Spacer(minLength: 4)
                    if let last = agent.lastMessage, !agent.state.working {
                        Text(last.createdAt.shortAgo()).font(.system(size: 11)).foregroundStyle(.secondary)
                    }
                }
                HStack(spacing: 4) {
                    if agent.needsYou {
                        Image(systemName: "exclamationmark.circle.fill").foregroundStyle(Theme.amber)
                            .font(.system(size: 11))
                    }
                    Text(preview)
                        .font(.system(size: 12))
                        .foregroundStyle(agent.state.working ? Theme.blue : .secondary)
                        .lineLimit(1)
                    Spacer(minLength: 0)
                    if let unread = agent.unreadCount, unread > 0 {
                        Text(unread > 99 ? "99+" : String(unread))
                            .font(.system(size: 11, weight: .bold))
                            .foregroundStyle(.white)
                            .padding(.horizontal, 6).padding(.vertical, 1)
                            .background(Theme.blue, in: Capsule())
                            .accessibilityLabel("\(unread) unread messages")
                    }
                }
            }
        }
        .padding(.vertical, 3)
        .accessibilityElement(children: .combine)
    }

    private var preview: String {
        if agent.state.working {
            if let text = streaming.values.first, !text.isEmpty { return Self.plain(text) }
            return agent.state.detail ?? "Working…"
        }
        guard let last = agent.lastMessage else { return "No messages yet" }
        if last.text.isEmpty, !last.files.isEmpty {
            return last.files.count == 1 ? "Sent \(last.files[0].name)" : "Sent \(last.files.count) files"
        }
        return (last.role == .user ? "You: " : "") + Self.plain(last.text)
    }

    /// One line of text without Markdown emphasis or code marks.
    static func plain(_ s: String) -> String {
        let attributed = try? AttributedString(markdown: s, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace))
        let text = attributed.map { String($0.characters) } ?? s
        return text.replacingOccurrences(of: "\n", with: " ")
    }
}

struct MacPairView: View {
    @Environment(AppStore.self) private var store

    var body: some View {
        let p = store.state.pairing
        VStack(alignment: .leading, spacing: 20) {
            VStack(alignment: .leading, spacing: 6) {
                Text("Ganclaw").font(.system(size: 30, weight: .bold)).tracking(-0.5)
                Text("A private messenger for your agents.").font(.system(size: 15)).foregroundStyle(Theme.text2)
            }
            VStack(alignment: .leading, spacing: 6) {
                Text("On your server, run").font(.system(size: 13, weight: .medium)).foregroundStyle(Theme.text2)
                Text("ganclaw app pair --name mac")
                    .font(.system(size: 13, design: .monospaced))
                    .padding(10)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Theme.chip, in: RoundedRectangle(cornerRadius: 8))
                    .textSelection(.enabled)
            }
            Form {
                TextField("Server", text: Binding(get: { store.state.pairing.server }, set: { store.send(.pairFields(server: $0, code: store.state.pairing.code)) }),
                          prompt: Text("https://ganclaw.example.com"))
                    .control("pair.server")
                TextField("Pairing code", text: Binding(get: { store.state.pairing.code }, set: { store.send(.pairFields(server: store.state.pairing.server, code: $0.uppercased())) }),
                          prompt: Text("ABCD-EFGH"))
                    .control("pair.code")
            }
            .textFieldStyle(.roundedBorder)
            .autocorrectionDisabled()
            .onSubmit { Task { await store.pair() } }

            if let e = p.error {
                Text(e).font(.system(size: 13)).foregroundStyle(Theme.red).control("pair.error", label: e)
            }
            HStack {
                Text("Or open the ganclaw:// link it prints.").font(.system(size: 12)).foregroundStyle(Theme.muted)
                Spacer()
                if p.busy { ProgressView().controlSize(.small) }
                CButton("pair.submit") { Task { await store.pair() } } label: {
                    Text(p.busy ? "Pairing…" : "Pair this Mac")
                }
                .buttonStyle(.borderedProminent)
                .keyboardShortcut(.defaultAction)
                .disabled(p.busy)
            }
        }
        .padding(28)
        .frame(width: 440)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 16, style: .continuous).stroke(Theme.line))
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.ground)
    }
}
