# Outbound build/test worker proof of concept

## Architecture inspected and proposal

Inspection of main at `902eeda` found:

- `cmd/ganclaw/run.go` starts configured Telegram bots, the agent router and
  housekeeping. There is no worker-only branch in `serve`.
- `internal/api/server.go` serves HTTP on a private Unix socket; `peercred.go`
  checks Linux SO_PEERCRED. It is neither an authenticated Internet API nor a
  portable Mac transport. Its peer-credential implementation uses Linux-specific
  symbols; the dedicated worker avoids those gateway imports.
- `internal/agenttools/agenttools.go` implements `ask_agent` and `hand_off` by
  calling `/v1/run` on that local socket. These tools address agents in the
  same router; separate Ganclaw instances do not discover or communicate with
  each other.
- `internal/procexec` offers replaceable subprocess dependencies, but its
  cancellation targets the process rather than its whole process group.
  The existing API's idempotency records release failed requests and are
  periodically pruned; that behavior is unsuitable for replay-safe device jobs.
- `config.MCPServers` already supports optional external stdio tools. Existing
  Linux agents can invoke a dedicated CLI now, and a later MCP adapter can use
  this extension without creating a second agent gateway.

Proposal: keep all agent execution, scheduling and customer messaging in the
existing Linux gateway. Add an opt-in build queue owned by Linux, addressed by
worker name. A Mac process runs only fixed local build/test profiles and sends
results. The isolated POC is `cmd/ganclaw-worker`: its `broker` mode is a Linux
queue sidecar, and its `worker` mode runs as the normal Mac user. Neither mode
imports or starts the gateway. It deliberately leaves `ganclaw serve`, the
production configuration and existing gateway API unchanged.

For a future integrated version, mount only the worker routes on a separate
TLS listener owned by `serve`, and expose submit/status/cancel/artifact through
its local socket/MCP. Do not put send/run/customer routes on that listener.
The POC controller credential is scoped to the queue, not to Telegram or agents.

| Transport | Benefits | Costs |
| --- | --- | --- |
| Outbound persistent WebSocket | Immediate dispatch/cancel; lower idle overhead | Keepalives, proxy timeouts, sleep reconnects, stream backpressure; durable queue and acknowledgements still required |
| Outbound HTTPS polling (chosen) | Standard HTTPS, NAT friendly, straightforward request retries and artifact upload | About 2 seconds idle dispatch/cancel latency; network request can add up to 30 seconds; polling writes heartbeat state |

There are no Mac inbound ports, public SSH, VPN or router changes. The Mac
needs outbound HTTPS to an isolated Linux endpoint. Linux needs an accessible
HTTPS listener or existing HTTPS reverse-proxy route; this POC does not create
one in production.

## Scope and recovery guarantees

- Named workers have separate tokens and profile allowlists. A separate
  controller token can submit, inspect, cancel, resolve and download jobs.
  Workers cannot submit jobs, read controller endpoints, address other workers
  or message customers. Credentials live in private files, never command args.
- All jobs on a worker run serially (stricter than one active device test).
  Worker instances hold an exclusive filesystem lock. Assignment also binds
  to a durable instance ID, preventing another state directory from taking it
  over with the same worker token. Do not clone an active worker's state/ID.
- IDs are generated before submission and must be reused on request retry.
  Reusing an ID with a different worker/profile/revision/timeout is rejected.
- The broker fsyncs assignment before replying. The worker fsyncs a local
  journal before process launch. A lost poll reply returns the same assignment.
  Results and checksummed gzip/tar bundles are retained locally, uploaded
  separately, and replayed without rerunning commands after a lost response.
- Sleep/network loss never makes a job eligible for another execution.
  After 30 seconds without a heartbeat, status says `disconnected` with an
  explicit unknown-outcome warning. On reconnect, an intact worker resumes
  control/result delivery. A process restart with an unfinished journal reports
  `uncertain`, exit code -1, and blocks the worker. This conservative handling
  also covers a crash before process launch. It is not an exactly-once promise
  across machine/disk loss, cloned state or arbitrary external side effects.
- A deadline starts at assignment, uses absolute wall time, and is capped at
  two hours. An asleep worker observes the expired deadline on waking.
  Cancellation sets a durable flag; it is immediate for queued jobs and
  cooperative through polling for assigned jobs. Disconnected jobs retain
  their slot until the worker confirms an outcome. The broker never reports
  cancellation as completed merely because the caller requested it.
- Local command groups are killed on timeout/cancel and after command exit.
  Daemons that escape the group are forbidden in approved profiles. Device
  cleanup can be a fixed local command with its own 30-second deadline;
  failed cleanup reports `uncertain` and blocks further work. The iOS example
  shuts down its dedicated Simulator; Android starts its own emulator in the
  job process group. Reserve these devices for the worker.
- Terminal status, command exit code, diagnostic message, log and artifact
  SHA-256/size are available through the CLI. Logs are capped at 16 MiB with
  a truncation marker. Bundle input and compressed output are capped at
  256 MiB. Upload/download verify SHA-256. No partial upload is acknowledged.
  Symlinks and nonregular artifacts are rejected; `os.Root` confines reads.
- No caller-provided executable, shell command, arguments, working directory,
  environment, repository URL or artifact paths are accepted for execution.
  Mac-owned JSON profiles contain absolute executables and directories.
  Optional revision pins require a clean local checkout at a full commit hash;
  no remote fetch/checkout occurs. Only approve reviewed code: Xcode build
  phases and Gradle are themselves executable code. This is a trusted-build
  executor, not a sandbox for hostile repositories or a defense against a
  malicious process running as the same Mac user.
- Job processes receive HOME/USER/LOGNAME/PATH/TMPDIR/LANG plus explicit
  profile environment and job IDs/paths, not inherited agent/worker secrets.
  Use a dedicated checkout without publishing credentials. The POC does not
  sign, archive for distribution, publish, or install production services.

The POC intentionally has no automatic rescheduling, retention collector,
streaming logs, multipart/resumable artifacts, per-agent controller ACLs,
remote source synchronization or multi-broker HA. State is a bounded
10,000-record atomic JSON queue, locked to one broker process, instead of a
new production SQLite migration. Large xcresults need a larger reviewed
limit or selective exports. Persist both state directories; never delete them
as a retry mechanism. Clocks should be synchronized. Queue/disk errors fail
closed. Back up state only when the broker is stopped.

## Linux setup (isolated)

Build from this branch, without replacing the production gateway:

```sh
go build -o "$HOME/bin/ganclaw-worker" ./cmd/ganclaw-worker
```

Create a private directory and two independent credentials (do not print them):

```sh
umask 077
mkdir -p "$HOME/worker-poc"
openssl rand -hex 32 > "$HOME/worker-poc/controller.token"
openssl rand -hex 32 > "$HOME/worker-poc/mac.token"
```

Obtain an HTTPS certificate for the **isolated endpoint** using your existing
certificate process. Install it and its private key in a private test directory.
Example `broker.json` (replace paths/host; no actual credentials in JSON):

```json
{
  "listen": "127.0.0.1:9443",
  "certificate": "/home/alex/worker-poc/fullchain.pem",
  "key": "/home/alex/worker-poc/privkey.pem",
  "state_dir": "/home/alex/worker-poc/queue",
  "controller_token_file": "/home/alex/worker-poc/controller.token",
  "workers": {
    "mac": {
      "token_file": "/home/alex/worker-poc/mac.token",
      "profiles": ["smoke", "failure", "ios-build", "ios-test", "android-build", "android-test"]
    }
  }
}
```

Run `ganclaw-worker broker --config /absolute/path/broker.json` as the Linux
user. For external Mac access, bind a chosen test address/port with TLS or
explicitly arrange a separate existing reverse-proxy HTTPS route. A loopback
listener alone cannot be reached from the Mac. Do not expose the gateway's
Unix API or change production configuration during exploration. Set upload
limits/timeouts on an approved proxy to accommodate the bundle limit.

On an endpoint with a test CA, trust that CA using `SSL_CERT_FILE=/path/ca.pem`
in the worker/controller process. Certificate and hostname verification remain
enabled; there is no insecure TLS mode, HTTP fallback or redirect following.
TLS 1.2 is the minimum. Revoke/rotate a worker token by updating its private
file and restarting the isolated broker; queued assignments persist.

## Mac setup (normal user)

The Mac must have Xcode with its license/first launch complete, installed iOS
Simulator runtimes, Android SDK/emulator, a compatible JDK and the project
Gradle wrapper. Run initial Simulator and emulator setup interactively as the
normal logged-in user. Do not use root or a system LaunchDaemon. A user
LaunchAgent can keep the worker alive in that login session.

Build the dedicated executable directly on the Mac:

```sh
go build -o "$HOME/bin/ganclaw-worker" ./cmd/ganclaw-worker
```

Or cross-build just this executable on Linux and transfer it through an
existing authorized channel (no inbound Mac port needed):

```sh
GOOS=darwin GOARCH=arm64 go build -o /tmp/ganclaw-worker-mac ./cmd/ganclaw-worker
# Use GOARCH=amd64 for an Intel Mac.
```

Securely transfer **only the Mac worker token**, not the controller credential.
Use private directories and `chmod 600` on the token/config. Install
`deploy/worker/native-job.sh` outside the reviewed app checkouts and `chmod 700`
it. Populate trusted app checkouts through your existing reviewed Git workflow.
The POC assumes a project with a shared Xcode scheme and an Android `app`
module; adjust only Mac-owned profiles/wrappers for different project layouts.

Example `worker.json` (replace all example paths, scheme, AVD and Simulator ID):

```json
{
  "url": "https://worker.example.com",
  "name": "mac",
  "token_file": "/Users/alex/worker-poc/mac.token",
  "state_dir": "/Users/alex/worker-poc/state",
  "profiles": {
    "smoke": {"command": "/usr/bin/uname", "args": ["-s"], "cwd": "/Users/alex/worker-poc"},
    "failure": {"command": "/usr/bin/false", "cwd": "/Users/alex/worker-poc"},
    "ios-build": {
      "command": "/Users/alex/worker-poc/native-job.sh", "args": ["ios-build"],
      "cwd": "/Users/alex/projects/sample-ios", "env": {"XCODE_SCHEME": "SampleApp"},
      "required_outputs": ["products"]
    },
    "ios-test": {
      "command": "/Users/alex/worker-poc/native-job.sh", "args": ["ios-test"],
      "cwd": "/Users/alex/projects/sample-ios",
      "env": {"XCODE_SCHEME": "SampleApp", "IOS_SIMULATOR_UDID": "REPLACE_WITH_RESERVED_SIMULATOR_ID"},
      "required_outputs": ["tests.xcresult"],
      "cleanup_command": "/usr/bin/xcrun",
      "cleanup_args": ["simctl", "shutdown", "REPLACE_WITH_RESERVED_SIMULATOR_ID"]
    },
    "android-build": {
      "command": "/Users/alex/worker-poc/native-job.sh", "args": ["android-build"],
      "cwd": "/Users/alex/projects/sample-android",
      "env": {"JAVA_HOME": "/path/to/jdk", "ANDROID_HOME": "/Users/alex/Library/Android/sdk"},
      "required_outputs": ["apk"]
    },
    "android-test": {
      "command": "/Users/alex/worker-poc/native-job.sh", "args": ["android-test"],
      "cwd": "/Users/alex/projects/sample-android",
      "env": {"JAVA_HOME": "/path/to/jdk", "ANDROID_HOME": "/Users/alex/Library/Android/sdk", "ANDROID_AVD": "worker_test", "ANDROID_APP_ID": "com.example.sample"},
      "required_outputs": ["androidTest-results", "final-screen.png"]
    }
  }
}
```

Start interactively:

```sh
"$HOME/bin/ganclaw-worker" worker --config "$HOME/worker-poc/worker.json"
```

For a LaunchAgent, use the following plist in
`~/Library/LaunchAgents/com.example.ganclaw-worker.plist`. Replace paths with
absolute paths (launchd does not expand `$HOME` or `~`). Keep logs private and
set a useful PATH explicitly for the Gradle/JDK toolchain.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.example.ganclaw-worker</string>
  <key>ProgramArguments</key><array>
    <string>/Users/alex/bin/ganclaw-worker</string><string>worker</string>
    <string>--config</string><string>/Users/alex/worker-poc/worker.json</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>30</integer>
  <key>EnvironmentVariables</key><dict><key>PATH</key><string>/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin</string></dict>
  <key>StandardOutPath</key><string>/Users/alex/worker-poc/worker.stdout.log</string>
  <key>StandardErrorPath</key><string>/Users/alex/worker-poc/worker.stderr.log</string>
</dict></plist>
```

Load only after the interactive smoke job works:

```sh
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.example.ganclaw-worker.plist"
```

No Telegram tokens, customer contacts, scheduled prompts or agent configuration
are needed on the Mac.

## Dispatch, evidence, cancellation and uncertain recovery

The Linux agent can use the CLI from its shell with a private queue controller
credential. Future MCP tools should wrap these same operations and enforce
per-agent profile permissions. This POC does not add tools to the running agent.

```sh
job_id=$(openssl rand -hex 16)
ganclaw-worker submit --url https://worker.example.com \
  --token-file "$HOME/worker-poc/controller.token" --id "$job_id" \
  --worker mac --profile ios-test --revision FULL_REVIEWED_COMMIT_HASH --timeout 1800
ganclaw-worker status --url https://worker.example.com \
  --token-file "$HOME/worker-poc/controller.token" --id "$job_id"
ganclaw-worker artifact --url https://worker.example.com \
  --token-file "$HOME/worker-poc/controller.token" --id "$job_id" \
  --output "$HOME/worker-poc/results.tar.gz"
ganclaw-worker cancel --url https://worker.example.com \
  --token-file "$HOME/worker-poc/controller.token" --id "$job_id"
```

`submit` reports acceptance, not test success. Poll `status` for the final
state and `result.exit_code`. A failed test remains a failed job even if artifact
upload succeeds. An artifact error also fails a job whose command exited 0;
its message distinguishes command success from missing evidence. Screenshots
and xcresults are in the tar bundle (`job.log`, `output/...`, optional
`artifacts/...`). Inspect/extract downloaded archives only in a fresh private
directory; do not extract over project or home files.

For `uncertain`:

1. Stop the Mac worker/LaunchAgent. Inspect the job log, device session and
   process list locally. Stop any surviving Xcode/XCTest/emulator processes and
   reset/shut down the reserved simulator/emulator. Preserve the job directory.
2. Once the old execution can no longer run, use the controller's `resolve`
   command with the job ID. This marks the queue entry failed but preserves its
   `uncertain` result/message; it does **not** rerun it.
3. Archive `state/journal.json` outside the active state path, keeping
   `state/instance.json`. Restart the worker. Submit a new job ID only if a
   deliberate retry is wanted. Never resolve based only on a missing heartbeat.

If the worker state was lost, restore it from a stopped-worker backup. Do not
silently assign an old job to a new instance. If ownership cannot be restored,
the POC requires explicit operator investigation/state recovery; automatic
lease expiry would violate the no-duplicate-execution guarantee.

## Functional UI tests for REST-only native apps

The transport cannot turn a unit test into a functional UI test. Configure the
shared iOS scheme to include an XCTest UI target, and Android instrumentation
to launch the actual Compose activity. Native UI drivers must interact with the
rendered app, assert UI state and REST-backed behavior, and attach screenshots
at meaningful checkpoints and on assertion failure. A screenshot alone is not
a functional assertion. Neither platform needs web browser testing.

For Swift/SwiftUI use accessibility identifiers, `XCUIApplication`,
`waitForExistence`, tap/type/swipe and `XCTAssert...`. Attach a rendered screenshot
with lifetime `.keepAlways`, including before an assertion that may fail:

```swift
let app = XCUIApplication()
app.launchEnvironment["UI_TEST_REST_URL"] = fixtureServerURL
app.launch()
let button = app.buttons["refresh"]
XCTAssertTrue(button.waitForExistence(timeout: 10))
button.tap()
let loaded = app.staticTexts["loaded-result"].waitForExistence(timeout: 10)
let shot = XCTAttachment(screenshot: app.screenshot())
shot.name = "after-refresh"
shot.lifetime = .keepAlways
add(shot)
XCTAssertTrue(loaded)
```

Have the XCTest target start a deterministic local REST fixture server and
assert its received requests. App URL overrides and deterministic clocks/data
must be compiled only under `#if UI_TESTING`; use a dedicated test configuration
in the actual project. Do not rely only on a runtime launch argument. Publishing
configurations must omit `UI_TESTING`, exclude fixture resources/test targets,
and have a release check that rejects fixture endpoints/control entry points.
The sample wrapper adds the compile condition only to its `ios-test` profile;
there is no publishing profile. Preserve `.xcresult`, which contains UI test
attachments even when xcodebuild fails. Apple documents
[result bundles](https://developer.apple.com/documentation/xcode/running-tests-and-interpreting-results)
and [XCUIScreenshot attachments](https://developer.apple.com/documentation/XCUIAutomation/XCUIScreenshot).

For Kotlin/Compose use an `androidTest` suite with
`createAndroidComposeRule<MainActivity>()`, semantic tags, `performClick`,
`performTextInput`, `assertIsDisplayed`/`assertTextEquals` and synchronization
with the REST fixture. Drive the actual activity, not an isolated composable.
Capture rendered images with `onRoot().captureToImage()` (save as PNG) or
`UiDevice.takeScreenshot(file)` for the full device. Persist test images under
`getExternalFilesDir(null)/screenshots` so the wrapper can pull them. Add a
failure rule that captures screenshots before teardown. A REST fixture on the
Mac is reachable from the emulator through `10.0.2.2`; alternatively run the
fixture inside instrumentation. Assert request method/path/body and responses.
Keep fixture dependencies in `androidTestImplementation`, app overrides in
`src/debug` or a dedicated test-only build type, and permissions/cleartext
exceptions in a test-only manifest. Keep `src/release` free of control code,
fixture URLs and data. Verify the publishing APK/AAB lacks those classes/assets.
See [Compose testing](https://developer.android.com/develop/ui/compose/testing)
for the instrumented testing dependencies and API. Pin dependencies in the
actual app; this gateway POC adds none.

No app checkouts, XCTest targets, Compose suites or fixtures were supplied in
this workspace. These are integration requirements and examples, not a claim
that this branch has exercised either application's UI or publishing builds.

## Validation and real Mac acceptance procedure

Run the isolated smoke script on Linux or Mac:

```sh
go build -o /tmp/ganclaw-worker-poc ./cmd/ganclaw-worker
./deploy/worker/smoke.sh /tmp/ganclaw-worker-poc
```

It uses a temporary private directory, short-lived localhost certificate and
loopback port 19443. It starts the real executable in both modes, runs a real
process, retrieves/checks artifact bytes and confirms exit-7 failure propagation.
It cleans up both processes/state. Running it on Linux proves the Linux path;
printing `Darwin` on the Mac would prove the Mac process path, but still not
prove a Linux-to-Mac network round-trip.

For the actual two-machine demonstration:

1. Start the isolated Linux HTTPS broker and Mac worker above. Submit `smoke`
   from Linux and download its bundle; `job.log` must contain `Darwin`.
2. Submit `failure`; status must be `failed` with nonzero command exit code,
   and its log bundle must still download.
3. Submit a locally approved long-running test, interrupt only the worker's
   network or sleep the Mac, observe `disconnected`/unknown outcome, restore
   connectivity and retrieve the same job's result. Verify the wrapper's local
   execution-count evidence contains exactly one invocation. Never restart a
   job merely because the Mac is disconnected.
4. Repeat with worker process termination after acceptance. Restart using the
   same state: expect `uncertain`, with no re-execution or next queued job.
   Use the recovery procedure above.
5. Run `ios-build`, `ios-test`, `android-build` and `android-test` against exact
   reviewed commits. Force a real UI assertion failure on each platform and
   verify nonzero exits, screenshots, REST assertions and native result bundles.
   Inspect publishing outputs separately to verify fixture exclusion.

Automated Go tests exercise durable restart, alternate-instance rejection,
scoped authentication, real command exits, process-group deadlines, cancellation,
artifact confinement/missing evidence, checksummed upload/download, and a
simulated broker outage/reconnection and lost completion acknowledgement
that execute only once. Those
simulations are explicitly not physical sleep/network-loss or native-device tests.

The live Mac/network/native-app acceptance remains pending until an authorized
Mac execution channel or operator-run setup is available. The exploration does
not modify production services or configuration.
