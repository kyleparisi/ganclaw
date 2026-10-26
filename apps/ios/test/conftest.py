"""Harness for the iOS control-server tests.

The app runs in a booted iOS simulator; the simulator shares the Mac's
loopback, so the app reaches the fake ganclaw server on 127.0.0.1 and the
tests reach the app's control server the same way.

Environment (test/run.sh sets these):
  GANCLAW_SIM        simulator UDID (default: "booted")
  GANCLAW_BUNDLE_ID  default com.kyleparisi.ganclaw
  FAKESERVER_BIN     the built apps/ios/tools/fakeserver binary
  ARTIFACTS          directory for screenshots and logs (default test/artifacts)
"""
import json
import os
import re
import secrets
import socket
import subprocess
import tempfile
import time
from pathlib import Path

import pytest
import requests

SIM = os.environ.get("GANCLAW_SIM", "booted")
BUNDLE = os.environ.get("GANCLAW_BUNDLE_ID", "com.kyleparisi.ganclaw")
ARTIFACTS = Path(os.environ.get("ARTIFACTS", Path(__file__).parent / "artifacts"))
ARTIFACTS.mkdir(parents=True, exist_ok=True)


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def wait_for(fn, timeout=10.0, interval=0.1, what="condition"):
    """Polls fn until it returns something truthy and returns that."""
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        try:
            last = fn()
            if last:
                return last
        except (requests.RequestException, KeyError, IndexError, TypeError) as e:
            last = e
        time.sleep(interval)
    raise AssertionError(f"timed out waiting for {what}; last: {last!r}")


class FakeServer:
    """The scripted ganclaw (apps/ios/tools/fakeserver)."""

    def __init__(self):
        self.bin = os.environ.get("FAKESERVER_BIN") or str(Path(__file__).parent.parent / "build" / "fakeserver")
        self.dir = tempfile.mkdtemp(prefix="ganclaw-fake-")
        self.port, self.admin_port = free_port(), free_port()
        self.url = f"http://127.0.0.1:{self.port}"
        self.admin = f"http://127.0.0.1:{self.admin_port}"
        self.proc = None

    def start(self):
        log = open(ARTIFACTS / "fakeserver.log", "a")
        self.proc = subprocess.Popen(
            [self.bin, "-addr", f"127.0.0.1:{self.port}", "-admin", f"127.0.0.1:{self.admin_port}", "-dir", self.dir],
            stdout=log, stderr=subprocess.STDOUT)
        wait_for(lambda: requests.get(f"{self.url}/app/v1/health", timeout=1).ok, what="fake server")

    def stop(self):
        if self.proc:
            self.proc.terminate()
            self.proc.wait(timeout=10)
            self.proc = None

    def post(self, path, body=None):
        r = requests.post(self.admin + path, json=body or {}, timeout=10)
        r.raise_for_status()
        return r.json()

    def code(self):
        return self.post("/admin/pair")["code"]

    def prompts(self):
        return requests.get(self.admin + "/admin/requests", timeout=10).json()["requests"]


class App:
    """The app's control server."""

    def __init__(self, port, key):
        self.url = f"http://127.0.0.1:{port}"
        self.s = requests.Session()
        self.s.headers["Authorization"] = f"Bearer {key}"

    def get(self, path, **kw):
        r = self.s.get(self.url + path, timeout=30, **kw)
        r.raise_for_status()
        return r

    def post(self, path, body=None, ok=True):
        r = self.s.post(self.url + path, json=body or {}, timeout=60)
        data = r.json()
        if ok:
            assert r.status_code == 200, f"{path} → {r.status_code} {data}"
        return data

    def state(self):
        return self.get("/state").json()

    def tree(self):
        return self.get("/ui-tree").json()

    def ids(self):
        return [e.get("id") for e in self.tree()["elements"] if e.get("id")]

    def element(self, id):
        for e in self.tree()["elements"]:
            if e.get("id") == id:
                return e
        return None

    def tap(self, id=None, label=None):
        """Activates an on-screen element, retrying while the UI settles."""
        body = {"id": id} if id else {"label": label}
        def attempt():
            r = self.s.post(self.url + "/tap", json=body, timeout=30)
            return r.json() if r.status_code == 200 else None
        return wait_for(attempt, timeout=5, what=f"element {id or label} on screen ({self.state()['screen']})")

    def screen(self):
        return self.state()["screen"]

    def wait_screen(self, name, timeout=5):
        return wait_for(lambda: self.screen() == name, timeout=timeout, what=f"screen {name}")

    def thread(self, agent):
        return self.state()["threads"].get(agent, {})

    def messages(self, agent):
        return self.thread(agent).get("messages", [])

    def agent(self, name):
        return next(a for a in self.state()["agents"] if a["name"] == name)

    def screenshot(self, name):
        time.sleep(0.4)  # let animations finish
        png = self.get("/screenshot").content
        assert png[:8] == b"\x89PNG\r\n\x1a\n"
        (ARTIFACTS / f"{name}.png").write_bytes(png)
        return png


def simctl(*args, check=True):
    return subprocess.run(["xcrun", "simctl", *args], check=check, capture_output=True, text=True)


class Simulator:
    def __init__(self):
        self.key = secrets.token_hex(16)
        self.log = ARTIFACTS / "app.log"

    def launch(self):
        port = free_port()
        env = dict(os.environ)
        env.update({
            "SIMCTL_CHILD_GANCLAW_CONTROL_KEY": self.key,
            "SIMCTL_CHILD_GANCLAW_CONTROL_PORT": str(port),
            "SIMCTL_CHILD_GANCLAW_FAKE_SPEECH": "1",
            "SIMCTL_CHILD_GANCLAW_EPHEMERAL": "1",
        })
        subprocess.run(["xcrun", "simctl", "launch", "--terminate-running-process",
                        f"--stdout={self.log}", f"--stderr={self.log}", SIM, BUNDLE],
                       check=True, env=env, capture_output=True, text=True)
        app = App(port, self.key)
        wait_for(lambda: app.s.get(app.url + "/state", timeout=1).status_code == 200, timeout=30, what="control server")
        printed = self.log.read_text(errors="replace") if self.log.exists() else ""
        assert re.search(r"Control server: http://127\.0\.0\.1:\d+", printed), "app should print its control URL"
        return app

    def terminate(self):
        simctl("terminate", SIM, BUNDLE, check=False)


@pytest.fixture(scope="session")
def fake():
    f = FakeServer()
    f.start()
    f.post("/admin/seed")
    yield f
    f.stop()


@pytest.fixture(scope="session")
def sim():
    s = Simulator()
    yield s
    s.terminate()


@pytest.fixture(scope="session")
def app(sim):
    return sim.launch()


@pytest.fixture
def paired(app, fake):
    """An app paired with the fake server, on the inbox."""
    if app.state()["connection"] == "unpaired":
        app.post("/pair", {"server": fake.url, "code": fake.code()})
    wait_for(lambda: app.state()["connection"] == "connected", timeout=15, what="websocket connected")
    if app.state()["drive"]["active"]:
        app.post("/drive/exit")
    if app.state()["attach"]["presented"]:
        app.post("/attach/close")
    app.post("/navigate", {"route": "inbox"})
    app.post("/filter", {"filter": "all"})
    wait_for(lambda: app.state()["agents_loaded"], what="agents")
    return app


def last_agent_reply(app, agent, after_id=0):
    """The newest finished agent message in a thread newer than after_id."""
    for m in reversed(app.messages(agent)):
        if m["id"] > after_id and m["role"] == "agent" and m["status"] != "streaming":
            return m
    return None
