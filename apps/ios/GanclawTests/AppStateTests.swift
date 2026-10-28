import XCTest
@testable import Ganclaw

final class AppStateTests: XCTestCase {
    private func msg(_ id: Int64, _ thread: String = "infra", role: Role = .agent, text: String = "hi",
                     status: MessageStatus = .done, actions: [String] = [], answer: String? = nil, job: String? = nil) -> AppMessage {
        AppMessage(id: id, thread: thread, role: role, text: text, files: [], actions: actions, answer: answer,
                   status: status, job: job, replyTo: nil, provider: nil,
                   createdAt: Date(timeIntervalSince1970: Double(id)), updatedAt: Date(timeIntervalSince1970: Double(id)))
    }

    private func agent(_ name: String, last: AppMessage? = nil, working: Bool = false, needsYou: Bool = false) -> AgentSummary {
        AgentSummary(name: name, state: AgentState(working: working, detail: nil, since: nil), lastMessage: last, needsYou: needsYou)
    }

    func testReadEventsUpdateSharedCountsWithoutMovingBackward() throws {
        var state = AppState()
        var summary = agent("support")
        summary.lastReadId = 10
        summary.unreadCount = 3
        state.apply(.agentsLoaded([summary]))
        XCTAssertEqual(state.totalUnread, 3)
        let event = try JSON.decoder.decode(ServerEvent.self,
            from: Data(#"{"type":"read","thread":"support","read":{"last_read_id":20,"unread_count":1}}"#.utf8))
        state.apply(.event(event))
        XCTAssertEqual(state.agent("support")?.lastReadId, 20)
        XCTAssertEqual(state.totalUnread, 1)
        state.apply(.readState("support", ReadState(lastReadId: 15, unreadCount: 2)))
        XCTAssertEqual(state.totalUnread, 1)
        state.apply(.agentsLoaded([summary])) // An older GET finishes after the read event.
        XCTAssertEqual(state.agent("support")?.lastReadId, 20)
        XCTAssertEqual(state.totalUnread, 1)
    }

    func testAStaleInboxLoadKeepsANewerLastMessage() {
        var state = AppState()
        state.apply(.agentsLoaded([agent("trader", last: msg(5, "trader"))]))
        state.apply(.event(ServerEvent(type: "message", message: msg(9, "trader", text: "Back online."))))
        // The reconnect refresh started before message 9 and finishes after it.
        state.apply(.agentsLoaded([agent("trader", last: msg(5, "trader"))]))
        XCTAssertEqual(state.agent("trader")?.lastMessage?.text, "Back online.")
        state.apply(.agentsLoaded([agent("trader", last: msg(12, "trader", text: "Newer"))]))
        XCTAssertEqual(state.agent("trader")?.lastMessage?.id, 12)
    }

    func testUnreadFieldsAreBackwardCompatible() throws {
        let old = try JSON.decoder.decode(AgentSummary.self,
            from: Data(#"{"name":"support","state":{"working":false},"needs_you":false}"#.utf8))
        XCTAssertNil(old.unreadCount)
        let current = try JSON.decoder.decode(AgentSummary.self,
            from: Data(#"{"name":"support","state":{"working":false},"needs_you":false,"last_read_id":42,"unread_count":5}"#.utf8))
        XCTAssertEqual(current.lastReadId, 42)
        XCTAssertEqual(current.unreadCount, 5)
    }

    func testTasksFilterDoesNotReuseOrdinaryAgentRows() {
        var state = AppState()
        state.apply(.agentsLoaded([agent("support", working: true)]))
        XCTAssertEqual(state.filter, .all)
        state.apply(.setFilter(.tasks))
        XCTAssertTrue(state.inboxRows.isEmpty)
        XCTAssertEqual(InboxFilter.tasks.title, "Tasks")
        state.apply(.setFilter(.all))
        XCTAssertEqual(state.inboxRows.map(\.name), ["support"])
    }

    func testWorkerTaskDecodesActualLifecycleAndCursor() throws {
        let data = Data(#"{"id":"work","agent":"support","state":"running","progress":"Tool activity observed","cursor":42,"updated_at":"2026-01-01T00:00:00Z"}"#.utf8)
        var task = try JSON.decoder.decode(WorkerTask.self, from: data)
        XCTAssertTrue(task.isActive)
        XCTAssertEqual(task.cursor, 42)
        XCTAssertEqual(task.progress, "Tool activity observed")
        task.state = "completed"
        XCTAssertFalse(task.isActive)
        task.state = "failed"
        XCTAssertFalse(task.isActive)
    }

    func testInboxOrder() {
        var s = AppState()
        s.apply(.agentsLoaded([
            agent("builder"),
            agent("mailbox", last: msg(5, "mailbox")),
            agent("trader", last: msg(3, "trader"), working: true),
            agent("infra", last: msg(2), needsYou: true),
            agent("research", last: msg(9, "research")),
        ]))
        XCTAssertEqual(s.sortedAgents.map(\.name), ["infra", "trader", "research", "mailbox", "builder"])
        s.apply(.setFilter(.running))
        XCTAssertEqual(s.inboxRows.map(\.name), ["trader"])
        s.apply(.setFilter(.needsYou))
        XCTAssertEqual(s.inboxRows.map(\.name), ["infra"])
    }

    func testMessageEventsUpsertAndStream() {
        var s = AppState()
        s.apply(.agentsLoaded([agent("infra")]))
        s.apply(.threadLoaded("infra", [msg(1, role: .user, text: "what broke?")], .idle))

        s.apply(.event(ServerEvent(type: "message", message: msg(2, text: "", status: .streaming))))
        s.apply(.event(ServerEvent(type: "delta", id: 2, thread: "infra", text: "The queue")))
        XCTAssertEqual(s.thread("infra").streaming[2], "The queue")
        XCTAssertEqual(s.thread("infra").messages.map(\.id), [1, 2])

        s.apply(.event(ServerEvent(type: "message", message: msg(2, text: "The queue times out.", actions: ["Approve"]))))
        XCTAssertNil(s.thread("infra").streaming[2], "finished replies stop streaming")
        XCTAssertEqual(s.thread("infra").messages.last?.text, "The queue times out.")
        XCTAssertEqual(s.agent("infra")?.lastMessage?.id, 2)
        XCTAssertTrue(s.agent("infra")?.needsYou == true)

        s.apply(.event(ServerEvent(type: "message", message: msg(2, text: "The queue times out.", actions: ["Approve"], answer: "Approve"))))
        XCTAssertFalse(s.agent("infra")?.needsYou == true, "answered buttons don't need you")
    }

    func testUnloadedThreadsDontCollectPartialHistory() {
        var s = AppState()
        s.apply(.agentsLoaded([agent("infra")]))
        s.apply(.event(ServerEvent(type: "message", message: msg(7))))
        XCTAssertTrue(s.thread("infra").messages.isEmpty)
        XCTAssertEqual(s.agent("infra")?.lastMessage?.id, 7, "the inbox still updates")
    }

    func testAgentStateEvents() {
        var s = AppState()
        s.apply(.agentsLoaded([agent("infra")]))
        s.apply(.event(ServerEvent(type: "agent", agent: "infra", state: AgentState(working: true, detail: "Writing", since: nil))))
        XCTAssertTrue(s.agent("infra")!.state.working)
        XCTAssertEqual(s.inboxRows.first?.name, "infra")
    }

    func testSentClearsDraftAndReply() {
        var s = AppState()
        s.apply(.threadLoaded("infra", [], .idle))
        s.apply(.draft("infra", "hello"))
        s.apply(.replyTo("infra", 4))
        s.apply(.sent(msg(5, role: .user, text: "hello")))
        XCTAssertEqual(s.thread("infra").draft, "")
        XCTAssertNil(s.thread("infra").replyTo)
        XCTAssertEqual(s.thread("infra").messages.map(\.id), [5])
    }

    func testAThreadCanQuoteAJobRunItAsksAbout() {
        var s = AppState()
        s.apply(.agentsLoaded([agent("infra")]))
        s.apply(.threadLoaded("infra", [msg(1)], .idle))
        let run = msg(8, text: "2 timeouts overnight.", job: "nightly")
        s.apply(.jobsLoaded([JobSummary(name: "nightly", agent: "infra", lastRun: run, runs: 1, problems: 0)]))
        XCTAssertNil(s.thread("infra").message(8), "job runs aren't in the conversation")
        XCTAssertEqual(s.quoted(8, in: "infra")?.text, "2 timeouts overnight.")
        s.apply(.jobRunsLoaded("nightly", [msg(6, text: "Older run", job: "nightly"), run]))
        XCTAssertEqual(s.quoted(6, in: "infra")?.text, "Older run")
        XCTAssertEqual(s.quoted(1, in: "infra")?.id, 1)
        XCTAssertFalse(msg(9, job: "background").isScheduledJob)
        XCTAssertTrue(run.isScheduledJob)
    }

    func testJobRunsUpdateFromEvents() {
        var s = AppState()
        s.apply(.agentsLoaded([agent("infra")]))
        s.apply(.threadLoaded("infra", [msg(1)], .idle))
        s.apply(.jobsLoaded([JobSummary(name: "nightly", agent: "infra", lastRun: msg(3, job: "nightly"), runs: 1, problems: 0)]))
        s.apply(.jobRunsLoaded("nightly", [msg(3, job: "nightly")]))
        s.apply(.event(ServerEvent(type: "job", message: msg(8, job: "nightly"))))
        XCTAssertEqual(s.thread("infra").messages.map(\.id), [1])
        XCTAssertNotEqual(s.agent("infra")?.lastMessage?.id, 8)
        XCTAssertEqual(s.jobs[0].runs, 2)
        XCTAssertEqual(s.jobs[0].lastRun.id, 8)
        XCTAssertEqual(s.jobRuns["nightly"]?.map(\.id), [8, 3])
    }

    func testScreens() {
        var s = AppState()
        XCTAssertEqual(s.currentScreen, "pair")
        s.apply(.paired(server: URL(string: "https://g.example")!, deviceName: "phone"))
        XCTAssertEqual(s.currentScreen, "inbox")
        s.apply(.push(.thread("infra")))
        XCTAssertEqual(s.currentScreen, "thread:infra")
        s.apply(.attachOpen("infra"))
        XCTAssertEqual(s.currentScreen, "attach")
        s.apply(.attachClose)
        s.apply(.driveStart("infra"))
        XCTAssertEqual(s.currentScreen, "drive")
        s.apply(.driveExit)
        s.apply(.unpaired(reason: "gone"))
        XCTAssertEqual(s.currentScreen, "pair")
        XCTAssertEqual(s.pairing.error, "gone")
    }

    func testDriveFlow() {
        var s = AppState()
        s.apply(.driveStart("infra"))
        XCTAssertEqual(s.drive.phase, .listening)
        s.apply(.driveHeard("did the fix"))
        s.apply(.driveAsked("Did the fix ship?", after: 9))
        XCTAssertEqual(s.drive.phase, .thinking)
        XCTAssertEqual(s.drive.heard, "")
        s.apply(.driveAnswered("Yes.", details: true))
        XCTAssertEqual(s.drive.phase, .speaking)
        XCTAssertEqual(s.drive.lastAnswer, "Yes.")
        s.apply(.driveShortAnswers(false))
        s.apply(.driveExit)
        XCTAssertFalse(s.drive.active)
        XCTAssertFalse(s.drive.shortAnswers, "the short-answers choice survives exit")
    }

    func testSpokenPart() {
        let m = msg(1, text: "Yes, it's live.\n---\nDeployed at 7:58.")
        XCTAssertEqual(m.spokenPart.spoken, "Yes, it's live.")
        XCTAssertTrue(m.spokenPart.hasDetails)
        XCTAssertFalse(msg(2, text: "Just this.").spokenPart.hasDetails)
    }

    func testSegments() {
        let s = Segment.parse("Here's a draft.\n\n```subject\nMoving our call\n```\n\nThen:\n```shell\nls -la\n```")
        XCTAssertEqual(s, [.prose("Here's a draft."), .code(info: "subject", text: "Moving our call"), .prose("Then:"), .code(info: "shell", text: "ls -la")])
        XCTAssertEqual(Segment.parse("```go\nfunc"), [.code(info: "go", text: "func")], "unclosed fences render while streaming")
    }

    func testPairLink() {
        let link = PairLink(URL(string: "ganclaw://pair?code=GDK7-BGTN&server=https%3A%2F%2Fganclaw.example.com")!)
        XCTAssertEqual(link?.code, "GDK7-BGTN")
        XCTAssertEqual(link?.server.absoluteString, "https://ganclaw.example.com")
        XCTAssertNil(PairLink(URL(string: "ganclaw://pair?code=X")!))
        XCTAssertNil(PairLink(URL(string: "ganclaw://pair?code=X&server=ftp://x")!))
    }

    func testDecodesServerJSON() throws {
        let json = """
        {"messages":[{"id":11,"thread":"infra","role":"agent","text":"hi","files":[{"id":"f1","name":"fix.diff","mime":"text/x-diff","size":10}],
        "actions":["Approve"],"status":"done","reply_to":3,"created_at":"2026-10-26T01:04:54.518Z","updated_at":"2026-10-26T01:04:54.518123456Z"}],
        "state":{"working":true,"detail":"Writing","since":"2026-10-26T01:04:54.518123456-04:00"}}
        """
        let page = try JSON.decoder.decode(APIClient.ThreadPage.self, from: Data(json.utf8))
        XCTAssertEqual(page.messages[0].replyTo, 3)
        XCTAssertEqual(page.messages[0].files[0].badge, "DIFF")
        XCTAssertEqual(page.messages[0].pendingActions, ["Approve"])
        XCTAssertTrue(page.state.working)
        XCTAssertNotNil(page.state.since)
    }

    func testInitials() {
        XCTAssertEqual(AgentNames.initials("infra"), "IN")
        XCTAssertEqual(AgentNames.initials("research"), "RS")
        XCTAssertEqual(AgentNames.initials("trader"), "TR")
        XCTAssertEqual(AgentNames.initials("log-checker"), "LC")
    }
}
