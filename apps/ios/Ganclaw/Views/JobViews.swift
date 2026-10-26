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
        .toolbar(.hidden, for: .navigationBar)
        .accessibilityIdentifier("screen.jobs")
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
                .accessibilityIdentifier("jobs.empty")
            }
            ForEach(jobs) { j in
                Button { store.openJob(j.name) } label: { JobRow(job: j) }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("jobs.row.\(j.name)")
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
                        Button { showAll = true } label: {
                            Text("Show \(quiet.count) quiet \(quiet.count == 1 ? "run" : "runs") · all OK")
                                .font(.system(size: 14, weight: .medium))
                                .frame(maxWidth: .infinity).frame(height: 44)
                                .background(Theme.segment, in: RoundedRectangle(cornerRadius: 12))
                                .foregroundStyle(Theme.text2)
                        }
                        .accessibilityIdentifier("job.showAll")
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
        .toolbar(.hidden, for: .navigationBar)
        .accessibilityIdentifier("screen.job")
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
                    Button { store.ask(about: run) } label: {
                        Text("Ask about this run").font(.system(size: 14, weight: .semibold))
                            .frame(maxWidth: .infinity).frame(height: 44)
                            .background(Theme.ink, in: RoundedRectangle(cornerRadius: 12))
                            .foregroundStyle(.white)
                    }
                    .accessibilityIdentifier("job.ask.\(run.id)")
                    ForEach(run.pendingActions, id: \.self) { label in
                        Button { Task { await store.choose(label, on: run) } } label: {
                            Text(label).font(.system(size: 14, weight: .medium))
                                .frame(maxWidth: .infinity).frame(height: 44)
                                .background(Theme.card, in: RoundedRectangle(cornerRadius: 12))
                                .overlay(RoundedRectangle(cornerRadius: 12).stroke(Theme.line))
                                .foregroundStyle(Theme.ink)
                        }
                        .accessibilityIdentifier("action.\(run.id).\(label)")
                    }
                }
            } else if RunKind(run) != .ok {
                Button("Ask about this run") { store.ask(about: run) }
                    .font(.system(size: 14, weight: .medium))
                    .foregroundStyle(Theme.blue)
                    .accessibilityIdentifier("job.ask.\(run.id)")
            }
        }
        .padding(14)
        .background(Theme.card, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(Theme.line))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("run.\(run.id)")
    }
}

struct BackHeader: View {
    var title: String
    var subtitle: String
    @Environment(AppStore.self) private var store

    var body: some View {
        HStack(spacing: 8) {
            Button { store.send(.pop) } label: {
                Image(systemName: "chevron.left").font(.system(size: 20, weight: .semibold)).frame(width: 44, height: 44)
            }
            .foregroundStyle(Theme.ink)
            .accessibilityLabel("Back")
            .accessibilityIdentifier("header.back")
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
