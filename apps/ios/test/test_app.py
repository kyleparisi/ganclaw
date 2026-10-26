"""Drives the iOS app through its control server against the fake ganclaw.

UI steps go through /tap (the real accessibility elements on screen);
things a simulator can't do by tapping (pick photos, speak) go through
intent routes. Each test leaves screenshots in ARTIFACTS for review.
"""
import base64
import time

import requests

from conftest import last_agent_reply, wait_for


def test_control_server_requires_its_key(app):
    assert requests.get(app.url + "/state", timeout=5).status_code == 401
    assert requests.get(app.url + "/state", headers={"Authorization": "Bearer nope"}, timeout=5).status_code == 401
    assert app.get("/state").status_code == 200


def test_pairing(app, fake):
    if app.state()["connection"] != "unpaired":
        app.post("/unpair")
    app.wait_screen("pair")
    assert "pair.submit" in app.ids()
    app.screenshot("01-pair")

    r = app.post("/pair", {"server": fake.url, "code": "WRNG-CODE"}, ok=False)
    assert r["ok"] is False
    assert "pairing code" in app.state()["pairing"]["error"]
    wait_for(lambda: app.element("pair.error"), what="error on screen")

    app.post("/pair", {"server": fake.url, "code": fake.code()})
    app.wait_screen("inbox")
    wait_for(lambda: app.state()["connection"] == "connected", timeout=15, what="websocket")
    s = app.state()
    assert {a["name"] for a in s["agents"]} == {"builder", "infra", "mailbox", "research", "trader"}


def test_inbox_matches_the_design(paired, fake):
    app = paired
    rows = app.state()["inbox_rows"]
    assert rows[0] == "infra", "the agent waiting on an approval comes first"
    assert rows[-1] == "builder", "agents that never talked come last"
    ids = app.ids()
    for agent in rows:
        assert f"inbox.row.{agent}" in ids
    assert "inbox.jobs" in ids, "scheduled jobs row"
    assert "inbox.drive" in ids and "inbox.talk" in ids
    app.screenshot("02-inbox")

    app.tap("filter.needsYou")
    wait_for(lambda: app.state()["inbox_rows"] == ["infra"], what="needs-you filter")
    app.tap("filter.jobs")
    wait_for(lambda: app.element("jobs.row.nightly-log-check"), what="jobs list")
    app.screenshot("03-inbox-jobs")
    app.tap("filter.all")


def test_open_thread_by_tapping(paired):
    app = paired
    app.tap("inbox.row.mailbox")
    app.wait_screen("thread:mailbox")
    wait_for(lambda: app.thread("mailbox").get("loaded"), what="history")
    files = [f for m in app.messages("mailbox") for f in m["files"]]
    assert files and files[0]["name"] == "invoice-oct.pdf"
    wait_for(lambda: app.element(f"file.{files[0]['id']}"), what="file card")
    app.tap("thread.back")
    app.wait_screen("inbox")


def test_send_message_and_stream_reply(paired):
    app = paired
    app.tap("inbox.row.trader")
    app.wait_screen("thread:trader")
    wait_for(lambda: app.thread("trader").get("loaded"), what="history")
    before = app.messages("trader")[-1]["id"]
    app.post("/type", {"agent": "trader", "text": "hello trader"})
    app.tap("composer.send")
    reply = wait_for(lambda: last_agent_reply(app, "trader", after_id=before), what="reply")
    assert reply["text"] == "Got it: hello trader"
    assert app.thread("trader")["draft"] == ""
    texts = [m["text"] for m in app.messages("trader")]
    assert texts[-2:] == ["hello trader", "Got it: hello trader"]
    app.screenshot("04-thread")


def test_running_state_and_stop(paired, fake):
    app = paired
    app.post("/navigate", {"route": "thread", "agent": "research"})
    app.post("/send", {"agent": "research", "text": "hold on, keep reading"})
    wait_for(lambda: app.agent("research")["state"]["working"], what="research working")
    status = wait_for(lambda: app.element("thread.status"), what="status line")
    assert status["label"].startswith("Working")
    app.screenshot("05-thread-working")

    app.post("/navigate", {"route": "inbox"})
    app.tap("filter.running")
    wait_for(lambda: app.state()["inbox_rows"] == ["research"], what="running filter")
    app.screenshot("06-inbox-running")
    app.tap("filter.all")

    app.post("/stop", {"agent": "research"})
    wait_for(lambda: not app.agent("research")["state"]["working"], what="stopped")
    wait_for(lambda: app.agent("research")["last_message"]["text"] == "Stopped.", what="stopped notice")


def test_approval_buttons(paired, fake):
    app = paired
    app.tap("inbox.row.infra")
    app.wait_screen("thread:infra")
    wait_for(lambda: app.thread("infra").get("loaded"), what="history")
    card = next(m for m in reversed(app.messages("infra")) if m["actions"] == ["Approve", "Not now"])
    app.screenshot("07-thread-approval")

    app.tap(f"action.{card['id']}.Approve")
    wait_for(lambda: next(m for m in app.messages("infra") if m["id"] == card["id"]).get("answer") == "Approve", what="answered")
    reply = wait_for(lambda: last_agent_reply(app, "infra", after_id=card["id"]), what="reply to approve")
    assert reply["text"].startswith("Opened PR #482")
    assert "[Replying to your earlier message:]" in fake.prompts()[-1]["prompt"]
    wait_for(lambda: app.element(f"answer.{card['id']}"), what="answer chip")
    assert not app.agent("infra")["needs_you"]


def test_copy_blocks(paired):
    app = paired
    app.post("/navigate", {"route": "thread", "agent": "mailbox"})
    before = app.messages("mailbox")[-1]["id"]
    app.post("/send", {"agent": "mailbox", "text": "draft a reply to the vendor"})
    reply = wait_for(lambda: last_agent_reply(app, "mailbox", after_id=before), what="draft")
    block = f"{reply['id']}.1"  # segments: prose, subject, body, shell
    wait_for(lambda: app.element(f"copy.{block}"), what="copy button")
    app.screenshot("08-copy-blocks")

    app.tap(f"copy.{block}")
    wait_for(lambda: app.state().get("copied_block") == block, what="copied state")
    assert app.state()["toast"] == "Subject copied"
    assert app.get("/clipboard").json()["text"] == "Moving our call to Thursday"
    app.screenshot("09-copied")

    app.tap(f"copy.{reply['id']}.3")
    wait_for(lambda: app.get("/clipboard").json()["text"].startswith("aws sqs get-queue-attributes"), what="shell copied")
    assert app.state()["toast"] == "Command copied"


def test_attachments(paired, fake):
    app = paired
    app.post("/navigate", {"route": "thread", "agent": "builder"})
    app.tap("composer.attach")
    app.wait_screen("attach")
    app.post("/attach/add", {"name": "notes.txt", "mime": "text/plain", "base64": base64.b64encode(b"build notes").decode()})
    png = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
    app.post("/attach/add", {"name": "screen.png", "mime": "image/png", "base64": base64.b64encode(png).decode()})
    wait_for(lambda: app.state()["attach"]["pending"] and all(p.get("file") for p in app.state()["attach"]["pending"]), what="uploads")
    count = wait_for(lambda: app.element("attach.count"), what="tray")
    assert count["label"] == "Ready to send · 2"
    app.post("/attach/note", {"text": "for the release"})
    app.screenshot("10-attach")

    app.tap("attach.send")
    app.wait_screen("thread:builder")
    sent = wait_for(lambda: next((m for m in app.messages("builder") if m["role"] == "user" and m["files"]), None), what="sent files")
    assert [f["name"] for f in sent["files"]] == ["notes.txt", "screen.png"]
    assert sent["text"] == "for the release"
    reply = wait_for(lambda: last_agent_reply(app, "builder", after_id=sent["id"]), what="reply")
    assert reply["text"] == "Got notes.txt, screen.png."
    app.screenshot("11-thread-files")


def test_drive_mode(paired, fake):
    app = paired
    app.post("/navigate", {"route": "thread", "agent": "infra"})
    app.tap("thread.audio")
    app.wait_screen("drive")
    d = app.state()["drive"]
    assert d["agent"] == "infra" and d["phase"] == "listening" and d["short_answers"]
    app.screenshot("12-drive-listening")

    app.post("/drive/say", {"text": "did the fix", "final": False})
    wait_for(lambda: app.state()["drive"]["heard"] == "did the fix", what="live transcript")
    app.post("/drive/say", {"text": "Did the fix ship?"})
    wait_for(lambda: app.state()["drive"]["last_question"] == "Did the fix ship?", what="question")
    answer = wait_for(lambda: app.state()["drive"]["last_answer"], timeout=15, what="spoken answer")
    assert answer == "Yes, it's live. The alarm is set. Nothing needs you right now."
    s = app.state()
    assert s["drive"]["details_saved"], "the part after --- stays in the thread"
    assert answer in s["speech"]["spoken"]
    assert fake.prompts()[-1]["prompt"].startswith("[The user is driving")
    app.screenshot("13-drive-answer")
    wait_for(lambda: app.state()["drive"]["phase"] == "listening", what="back to listening")

    app.tap("drive.mute")
    wait_for(lambda: app.state()["drive"]["muted"], what="muted")
    assert not app.state()["speech"]["listening"]
    app.post("/drive/say", {"text": "ignored while muted"})
    time.sleep(0.5)
    assert app.state()["drive"]["last_question"] == "Did the fix ship?"
    app.tap("drive.exit")
    app.wait_screen("thread:infra")


def test_jobs_and_ask_about_a_run(paired, fake):
    app = paired
    runs_before = next(j for j in app.state()["jobs"] if j["name"] == "nightly-log-check")["runs"]
    fake.post("/admin/run", {"agent": "infra", "prompt": "check the logs", "job": "nightly-log-check"})
    wait_for(lambda: next(j for j in app.state()["jobs"] if j["name"] == "nightly-log-check")["runs"] == runs_before + 1,
             what="new run pushed over the websocket")

    app.post("/navigate", {"route": "job", "job": "nightly-log-check"})
    app.wait_screen("job:nightly-log-check")
    runs = app.state()["job_runs"]["nightly-log-check"]
    latest = runs[0]
    wait_for(lambda: app.element(f"job.ask.{latest['id']}"), what="ask button")
    app.screenshot("14-job")

    app.tap(f"job.ask.{latest['id']}")
    app.wait_screen("thread:infra")
    assert app.thread("infra")["reply_to"] == latest["id"]
    wait_for(lambda: app.element("thread.replyBar"), what="reply bar")
    app.post("/send", {"agent": "infra", "text": "why?"})
    wait_for(lambda: "[Replying to your earlier message:]" in fake.prompts()[-1]["prompt"], what="reply context")
    assert app.thread("infra").get("reply_to") is None


def test_server_pushes_reach_the_inbox(paired, fake):
    app = paired
    fake.post("/admin/post", {"agent": "builder", "text": "Deploy finished."})
    wait_for(lambda: app.agent("builder")["last_message"]["text"] == "Deploy finished.", timeout=5, what="pushed message")


def test_reconnects_after_server_restart(paired, fake):
    app = paired
    fake.stop()
    wait_for(lambda: app.state()["connection"] == "disconnected", timeout=10, what="disconnect noticed")
    fake.start()
    wait_for(lambda: app.state()["connection"] == "connected", timeout=40, what="reconnected")
    fake.post("/admin/post", {"agent": "trader", "text": "Back online."})
    wait_for(lambda: app.agent("trader")["last_message"]["text"] == "Back online.", timeout=5, what="message after reconnect")
