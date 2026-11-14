import SwiftUI

/// Scheduled jobs are runs delivered with `ganclaw run --to app:<agent>
/// --session <job>`; the session name is the job's name.
struct JobsView: View {
    @Environment(AppStore.self) private var store

    var body: some View {
        VStack(spacing: 0) {
            BackHeader(title: "Scheduled jobs", subtitle: "\(store.state.jobs.count) jobs")
            ScrollView {
                JobList(jobs: store.state.jobs).padding(20)
            }
            .refreshable { await store.refresh() }
        }
        .background(Theme.ground.ignoresSafeArea())
        .hidesNavigationBar()
    }
}

struct JobList: View {
    var jobs: [JobSummary]
    @Environment(AppStore.self) private var store

    var body: some View {
        VStack(spacing: 8) {
            if jobs.isEmpty {
                VStack(spacing: 8) {
                    Text("No scheduled jobs yet.").font(.system(size: 15, weight: .semibold))
                    Text("Deliver a scheduled run here with\n`ganclaw run --agent infra --to app:infra --session nightly`")
                        .font(.system(size: 13)).foregroundStyle(Theme.muted).multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity).padding(.top, 40)
                .control("jobs.empty")
            }
            ForEach(jobs) { j in
                CButton("jobs.row.\(j.name)") { store.openJob(j.name) } label: { JobRow(job: j) }
                    .buttonStyle(.plain)
            }
        }
    }
}

struct JobRow: View {
    var job: JobSummary

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: "clock").font(.system(size: 18)).foregroundStyle(Theme.text2)
                .frame(width: 44, height: 44)
                .background(Theme.segment, in: RoundedRectangle(cornerRadius: 13, style: .continuous))
            VStack(alignment: .leading, spacing: 4) {
                HStack {
                    Text(jobTitle(job.name)).font(.system(size: 16, weight: .semibold))
                    Spacer()
                    RunBadge(run: job.lastRun)
                }
                Text("\(Theme.displayName(job.agent)) · \(job.runs) \(job.runs == 1 ? "run" : "runs") · last \(job.lastRun.createdAt.shortAgo())")
                    .font(.system(size: 13)).foregroundStyle(Theme.muted)
                Text(job.lastRun.text.replacingOccurrences(of: "\n", with: " "))
                    .font(.system(size: 14)).foregroundStyle(Theme.text2).lineLimit(2)
            }
        }
        .padding(14)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(Theme.line))
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }
}

func jobTitle(_ name: String) -> String {
    let words = name.split(whereSeparator: { $0 == "-" || $0 == "_" }).map(String.init)
    guard let first = words.first else { return name }
    return ([first.prefix(1).uppercased() + first.dropFirst()] + words.dropFirst()).joined(separator: " ")
}

enum RunKind {
    case failed, warning, ok

    init(_ m: AppMessage) {
        if m.status == .error { self = .failed }
        else if !m.pendingActions.isEmpty { self = .warning }
        else { self = .ok }
    }
}

struct RunBadge: View {
    var run: AppMessage

    var body: some View {
        let (text, fg, bg): (String, Color, Color) = switch RunKind(run) {
        case .failed: ("Failed", Theme.red, Theme.redSoft)
        case .warning: ("Warning", Theme.amberText, Theme.amberChip)
        case .ok: ("No issues", Theme.teal, Color(hex: 0xE5F3F1))
        }
        Text(text)
            .font(.system(size: 12, weight: .semibold))
            .foregroundStyle(fg)
            .padding(.horizontal, 8).padding(.vertical, 3)
            .background(bg, in: RoundedRectangle(cornerRadius: 6))
    }
}

struct JobView: View {
    var job: String
    @Environment(AppStore.self) private var store
    @State private var showAll = false

    var body: some View {
        let runs = store.state.jobRuns[job] ?? []
        let summary = store.state.jobs.first { $0.name == job }
        VStack(spacing: 0) {
            BackHeader(title: jobTitle(job), subtitle: summary.map { "\(Theme.displayName($0.agent)) · \($0.runs) runs" } ?? "")
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    let latest = runs.first
                    if let latest {
                        RunCard(run: latest, expanded: true)
                    }
                    let rest = Array(runs.dropFirst())
                    let problems = rest.filter { RunKind($0) != .ok }
                    let quiet = rest.filter { RunKind($0) == .ok }
                    ForEach(showAll ? rest : problems) { RunCard(run: $0, expanded: false) }
                    if !showAll && !quiet.isEmpty {
                        CButton("job.showAll") { showAll = true } label: {
                            Text("Show \(quiet.count) quiet \(quiet.count == 1 ? "run" : "runs") · all OK")
                                .font(.system(size: 14, weight: .medium))
                                .frame(maxWidth: .infinity).frame(height: 44)
                                .background(Theme.segment, in: RoundedRectangle(cornerRadius: 12))
                                .foregroundStyle(Theme.text2)
                        }
                    }
                    if runs.isEmpty {
                        ProgressView().frame(maxWidth: .infinity).padding(.top, 40)
                    }
                    Text("Reply to a run to start a conversation about it.")
                        .font(.system(size: 13)).foregroundStyle(Theme.muted)
                        .frame(maxWidth: .infinity).padding(.top, 8)
                }
                .padding(16)
            }
            .refreshable { await store.loadJobRuns(job) }
        }
        .background(Theme.ground.ignoresSafeArea())
        .hidesNavigationBar()
        .task(id: job) { if runs.isEmpty { await store.loadJobRuns(job) } }
    }
}

struct RunCard: View {
    var run: AppMessage
    var expanded: Bool
    @Environment(AppStore.self) private var store

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                RunBadge(run: run)
                Text(run.createdAt.formatted(.dateTime.month(.abbreviated).day().hour().minute()))
                    .font(.system(size: 13)).foregroundStyle(Theme.muted)
                Spacer()
            }
            if expanded || RunKind(run) != .ok {
                MessageContent(text: run.text, messageID: run.id)
                ForEach(run.files) { FileView(file: $0, onUser: false) }
            }
            if expanded {
                HStack(spacing: 8) {
                    CButton("job.ask.\(run.id)") { store.ask(about: run) } label: {
                        Text("Ask about this run").font(.system(size: 14, weight: .semibold))
                            .frame(maxWidth: .infinity).frame(height: 44)
                            .background(Theme.ink, in: RoundedRectangle(cornerRadius: 12))
                            .foregroundStyle(.white)
                    }
                    ForEach(run.pendingActions, id: \.self) { label in
                        CButton("action.\(run.id).\(label)") { Task { await store.choose(label, on: run) } } label: {
                            Text(label).font(.system(size: 14, weight: .medium))
                                .frame(maxWidth: .infinity).frame(height: 44)
                                .background(Theme.card, in: RoundedRectangle(cornerRadius: 12))
                                .overlay(RoundedRectangle(cornerRadius: 12).stroke(Theme.line))
                                .foregroundStyle(Theme.ink)
                        }
                    }
                }
            } else if RunKind(run) != .ok {
                CButton("job.ask.\(run.id)", title: "Ask about this run") { store.ask(about: run) }
                    .font(.system(size: 14, weight: .medium))
                    .foregroundStyle(Theme.blue)
            }
        }
        .padding(14)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(Theme.line))
        .accessibilityElement(children: .contain)
        .control("run.\(run.id)")
    }
}

struct BackHeader: View {
    var title: String
    var subtitle: String
    @Environment(AppStore.self) private var store

    var body: some View {
        HStack(spacing: 8) {
            CButton("header.back") { store.send(.pop) } label: {
                Image(systemName: "chevron.left").font(.system(size: 20, weight: .semibold)).frame(width: 44, height: 44)
            }
            .foregroundStyle(Theme.ink)
            .accessibilityLabel("Back")
            VStack(alignment: .leading, spacing: 1) {
                Text(title).font(.system(size: 17, weight: .semibold))
                if !subtitle.isEmpty { Text(subtitle).font(.system(size: 12)).foregroundStyle(Theme.muted) }
            }
            Spacer()
        }
        .padding(.horizontal, 12).padding(.vertical, 8)
        .overlay(alignment: .bottom) { Theme.line.frame(height: 1) }
    }
}


/// Task threads are read-only. Their final result also arrives in the
/// conversation that requested the work; polling never starts a worker turn.
struct TaskList: View {
    @Environment(AppStore.self) private var store
    @State private var archived = false
    @State private var tasks: [WorkerTask] = []
    @State private var selected: String?
    @State private var messages: [AppMessage] = []
    @State private var error: String?
    @State private var more = false
    @State private var olderMessages = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Toggle("Archived tasks", isOn: $archived).control("tasks.archived")
            if let error { Text(error).foregroundStyle(Theme.red) }
            if tasks.isEmpty { Text("No \(archived ? "archived" : "active") tasks.").foregroundStyle(Theme.muted).control("tasks.empty") }
            ForEach(tasks) { task in
                CButton("tasks.row.\(task.id)") {
                    selected = task.id
                    messages = []
                    Task { await loadMessages(task.id) }
                } label: {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("\(Theme.displayName(task.agent)) · \(task.state.capitalized)").font(.headline)
                        Text(task.progress).font(.subheadline)
                        Text("Observed \(task.updatedAt.shortAgo()) · \(task.id.prefix(8))").font(.caption).foregroundStyle(Theme.muted)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading).padding(14)
                    .background(Theme.card, in: RoundedRectangle(cornerRadius: 16))
                }.buttonStyle(.plain)
            }
            if more {
                CButton("tasks.more", title: "Load more tasks") { Task { await load(append: true) } }
            }
            if selected != nil {
                Text("Task results").font(.headline)
                if olderMessages {
                    CButton("tasks.older", title: "Load earlier messages") {
                        if let selected { Task { await loadMessages(selected, older: true) } }
                    }
                }
                ForEach(messages) { message in
                    MessageContent(text: message.text, messageID: message.id)
                    ForEach(message.files) { FileView(file: $0, onUser: false) }
                }
                if messages.isEmpty { Text("No result messages yet.").foregroundStyle(Theme.muted) }
            }
        }
        .task(id: archived) {
            selected = nil
            tasks = []
            while !Task.isCancelled {
                await load()
                if let selected { await loadMessages(selected) }
                try? await Task.sleep(for: .seconds(3))
            }
        }
    }

    @MainActor private func load(append: Bool = false) async {
        guard let api = store.api else { return }
        let mode = archived
        do {
            // Refresh all loaded pages, so pagination stays stable while polling.
            var page = try await api.tasks(archived: mode, before: append ? tasks.last?.cursor : nil)
            let targetCount = tasks.count
            var hasMore = page.count == 100
            while !append && hasMore && page.count < targetCount {
                let next = try await api.tasks(archived: mode, before: page.last?.cursor)
                page += next
                hasMore = next.count == 100
            }
            guard mode == archived, !Task.isCancelled else { return }
            tasks = append ? tasks + page : page
            more = hasMore
            error = nil
        } catch { if !Task.isCancelled { self.error = error.localizedDescription } }
    }

    @MainActor private func loadMessages(_ id: String, older: Bool = false) async {
        guard let api = store.api else { return }
        do {
            let page = try await api.taskMessages(id, before: older ? messages.first?.id : nil)
            guard selected == id else { return }
            if older { messages = page + messages; olderMessages = page.count == 100 }
            else {
                let known = Set(messages.map(\.id))
                messages += page.filter { !known.contains($0.id) }
                messages.sort { $0.id < $1.id }
                if known.isEmpty { olderMessages = page.count == 100 }
                // Replace existing messages too (files can arrive after the text).
                for message in page {
                    if let index = messages.firstIndex(where: { $0.id == message.id }) { messages[index] = message }
                }
            }
        } catch { self.error = error.localizedDescription }
    }
}

struct OpenItemsView: View {
    @Environment(AppStore.self) private var store
    @State private var items: [OpenItem] = []
    @State private var done = false
    @State private var editing: OpenItem?
    @State private var error: String?
    @State private var busy = false
    @State private var showRuns = false

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                Toggle("Completed", isOn: $done)
                CButton("items.add", title: "Add item") { editing = OpenItem() }
            }
            if let error { Text(error).foregroundStyle(Theme.red) }
            if items.isEmpty { Text(done ? "No completed items." : "No open items. Add a next step or something you're waiting on.").foregroundStyle(Theme.muted) }
            ForEach(done ? ["done"] : ["todo", "waiting"], id: \.self) { group in
                let grouped = items.filter { $0.state == group }
                if !grouped.isEmpty {
                    Text(group == "todo" ? "To do" : group == "waiting" ? "Waiting on" : "Completed").font(.headline)
                    ForEach(grouped) { item in
                        VStack(alignment: .leading, spacing: 8) {
                            CButton("items.edit.\(item.id)", title: item.title) { editing = item }.font(.headline).foregroundStyle(Theme.ink)
                            if !item.waitingOn.isEmpty { Text("Waiting on: \(item.waitingOn)").font(.subheadline) }
                            if !item.followUp.isEmpty { Text("Follow up: \(item.followUp)").font(.caption) }
                            HStack {
                                if store.state.agent(item.agent) != nil {
                                    CButton("items.thread.\(item.id)", title: Theme.displayName(item.agent)) { store.openThread(item.agent) }
                                }
                                Spacer()
                                CButton("items.complete.\(item.id)", title: done ? "Reopen" : "Complete") {
                                    var updated = item
                                    // Reopening returns a dependency to Waiting on rather than dropping it to To do.
                                    updated.state = done ? (item.waitingOn.isEmpty ? "todo" : "waiting") : "done"
                                    Task { if let message = await save(updated) { error = message } }
                                }.disabled(busy)
                            }.font(.subheadline)
                        }
                        .padding(14).frame(maxWidth: .infinity, alignment: .leading)
                        .background(Theme.card, in: RoundedRectangle(cornerRadius: 16))
                    }
                }
            }
            if !done {
                let approvals = store.state.sortedAgents.filter { $0.needsYou }
                if !approvals.isEmpty {
                    Text("Decisions").font(.headline)
                    ForEach(approvals) { agent in
                        CButton("inbox.row.\(agent.name)") { store.openThread(agent.name) } label: { AgentRow(agent: agent) }
                    }
                }
            }
            CButton("items.runs", title: showRuns ? "Hide background runs" : "Background runs") { showRuns.toggle() }
            if showRuns { TaskList() }
        }
        .task(id: done) {
            while !Task.isCancelled {
                await load()
                try? await Task.sleep(for: .seconds(5))
            }
        }
        .sheet(item: $editing) { item in
            OpenItemEditor(item: item, agents: store.state.agents.map(\.name), save: { await save($0) }, delete: { await delete($0) })
        }
    }

    @MainActor private func load() async {
        guard let api = store.api else { return }
        let mode = done
        do {
            let loaded = try await api.openItems(done: mode)
            guard mode == done, !Task.isCancelled else { return }
            items = loaded
            error = nil
        } catch { if !Task.isCancelled { self.error = error.localizedDescription } }
    }

    /// Returns an error message, or nil on success. Saves may overlap (a list
    /// action while the editor is open); `busy` only disables list buttons.
    @MainActor private func save(_ item: OpenItem) async -> String? {
        await mutate { _ = try await $0.saveOpenItem(item) }
    }

    @MainActor private func delete(_ item: OpenItem) async -> String? {
        await mutate { try await $0.deleteOpenItem(item.id) }
    }

    @MainActor private func mutate(_ body: (APIClient) async throws -> Void) async -> String? {
        guard let api = store.api else { return "Not connected" }
        busy = true
        defer { busy = false }
        do {
            try await body(api)
            await load()
            return nil
        } catch {
            if (error as? APIError)?.status == 409 { await load() }
            return error.localizedDescription
        }
    }
}

struct OpenItemEditor: View {
    @Environment(\.dismiss) private var dismiss
    @State var item: OpenItem
    var agents: [String]
    var save: (OpenItem) async -> String?
    var delete: (OpenItem) async -> String?
    @State private var saving = false
    @State private var failure: String?
    @State private var confirmDelete = false
    @State private var hasFollowUp = false
    @State private var followUp = Date()

    var body: some View {
        NavigationStack {
            Form {
                TextField("Next step", text: $item.title)
                Picker("Status", selection: $item.state) {
                    Text("To do").tag("todo")
                    Text("Waiting on").tag("waiting")
                    Text("Completed").tag("done")
                }
                if item.state == "waiting" { TextField("Who or what are we waiting on?", text: $item.waitingOn) }
                Picker("Conversation", selection: $item.agent) {
                    Text("None").tag("")
                    ForEach(Array(Set(agents + (item.agent.isEmpty ? [] : [item.agent]))).sorted(), id: \.self) { Text(Theme.displayName($0)).tag($0) }
                }
                Toggle("Follow-up date", isOn: $hasFollowUp)
                if hasFollowUp { DatePicker("Follow up", selection: $followUp, displayedComponents: .date) }
                Text("Follow-up dates appear on the list; they don't send reminders.").font(.caption)
                if let failure { Text(failure).foregroundStyle(Theme.red) }
                if !item.id.isEmpty {
                    CButton("items.delete", title: "Delete item") { confirmDelete = true }
                        .foregroundStyle(Theme.red).disabled(saving)
                }
            }
            .navigationTitle(item.id.isEmpty ? "Add item" : "Edit item")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { CButton("items.cancel", title: "Cancel") { dismiss() }.disabled(saving) }
                ToolbarItem(placement: .confirmationAction) {
                    CButton("items.save", title: "Save") {
                        saving = true
                        item.followUp = hasFollowUp ? Self.dateFormatter.string(from: followUp) : ""
                        Task {
                            failure = await save(item)
                            saving = false
                            if failure == nil { dismiss() }
                        }
                    }.disabled(saving || item.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || (item.state == "waiting" && item.waitingOn.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty))
                }
            }
        }
        .onAppear {
            hasFollowUp = !item.followUp.isEmpty
            followUp = Self.dateFormatter.date(from: item.followUp) ?? Date()
        }
        .interactiveDismissDisabled(saving)
        .confirmationDialog("Delete this item?", isPresented: $confirmDelete, titleVisibility: .visible) {
            Button("Delete", role: .destructive) {
                saving = true
                Task {
                    failure = await delete(item)
                    saving = false
                    if failure == nil { dismiss() }
                }
            }
        } message: { Text("It will be removed for you and your agents. To keep it in history, mark it Completed instead.") }
    }

    private static var dateFormatter: DateFormatter {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.calendar = Calendar(identifier: .gregorian)
        formatter.dateFormat = "yyyy-MM-dd"
        return formatter
    }
}
