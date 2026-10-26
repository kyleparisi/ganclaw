# CLAUDE.md — Ganclaw iOS app

Native SwiftUI client for ganclaw's app channel (`internal/appchannel`).
Design reference: the "Relay" canvas (inbox, thread, attach sheet, drive
mode, copy blocks, scheduled-job thread).

## Build & test

The Xcode project is generated from `project.yml` with xcodegen and is not
committed. Everything runs through one script, locally or in CI
(`.github/workflows/ios.yml`):

```bash
brew install xcodegen
apps/ios/test/run.sh                     # build, unit tests, control-server tests
SKIP_UNIT=1 PYTEST_ARGS="-k drive" apps/ios/test/run.sh
```

Screenshots of every tested screen land in `apps/ios/test/artifacts/`
(CI uploads them as the `ios-artifacts` artifact). Look at them when you
change UI.

## Control server (Debug builds only)

Like bufflehead's, the app embeds an HTTP control server for tests,
compiled only when `CONTROL_SERVER` is set (Debug). It binds 127.0.0.1 on
`GANCLAW_CONTROL_PORT` (default: a free port) and prints on launch:

```
Control server: http://127.0.0.1:<port>
Control key: <key>
```

Every request needs `Authorization: Bearer <key>`. The key is minted per
launch unless `GANCLAW_CONTROL_KEY` pins it (the harness does). Both are also
written to `<app tmp>/ganclaw-control.json`. Other launch variables:
`GANCLAW_FAKE_SPEECH=1` (drive mode without mic/speaker; `/drive/say`
injects speech) and `GANCLAW_EPHEMERAL=1` (nothing saved to the Keychain).
Pass them through simctl with the `SIMCTL_CHILD_` prefix.

The simulator shares the Mac's loopback, so curl works directly:

```bash
curl -H "Authorization: Bearer $KEY" http://127.0.0.1:$PORT/state
curl -H "Authorization: Bearer $KEY" http://127.0.0.1:$PORT/ui-tree
curl -H "Authorization: Bearer $KEY" -d '{"id":"inbox.row.infra"}' http://127.0.0.1:$PORT/tap
curl -H "Authorization: Bearer $KEY" http://127.0.0.1:$PORT/screenshot > s.png
```

Routes (`Ganclaw/Control/ControlRoutes.swift`):

- UI, acting like a user: `GET /ui-tree` (controls on screen with ids,
  labels, frames, enabled), `POST /tap {"id"|"label"}` (runs a button's
  action), `GET /screenshot`, `GET /clipboard`.
- State: `GET /state` (the whole `AppState` as snake_case JSON, plus
  `screen`, `inbox_rows`, and `speech` with fake speech), `GET /actions`.
- Intents (the same AppStore methods views call, for what a simulator
  can't tap): `/pair`, `/unpair`, `/refresh`, `/navigate`, `/filter`,
  `/type`, `/send`, `/choose`, `/stop`, `/reset`, `/attach/open|add|note|send|close`,
  `/drive/start|say|mute|short|repeat|exit`.

Prefer `/tap` in tests: it runs the same closure as the real button, and
only while that button is on screen and enabled. Build tappable controls
with `CButton("id") { action } label: { … }` and mark other views the tests
read with `.control("id", label:)` (`Control/ControlRegistry.swift`); in
Release both are just an accessibility identifier. Ids look like
`inbox.row.<agent>`, `composer.send`, `action.<message id>.<label>`,
`copy.<message id>.<segment>`; keep them stable. Don't put an id on a whole
screen: in SwiftUI a container's identifier overrides its children's.
(The tree is not read from SwiftUI's accessibility elements: those only
exist while an assistive technology is attached.)

The tests run against `tools/fakeserver`: ganclaw's real app channel with
scripted agents (canned replies streamed in chunks), plus admin routes to
seed history, start job runs and post messages.

## Architecture: state, then render

Same rule as bufflehead's UI: the screen is a projection of state.

- `Model/AppState.swift` — all UI state in one value. `apply(_ Action)` is
  the only way it changes and is pure (no I/O); unit-tested in
  `GanclawTests` (the model files also compile on Linux).
- `Model/AppStore.swift` — owns the state; runs effects (HTTP, WebSocket,
  uploads, speech) and feeds results back as actions. Never assign
  `state` fields directly; add an action.
- Views read `store.state` and call `store.send(...)` or an intent method.
  Navigation (`path`), sheets (`attach.presented`) and drive mode
  (`drive.active`) are state, bound through `Binding(get:set:)` that
  dispatch actions.
- `Net/` — `APIClient` (REST), `EventStream` (WebSocket with reconnect),
  Keychain credentials, `ganclaw://pair` links.
- `Drive/Speech.swift` — `SpeechEngine`: live (Speech + AVSpeechSynthesizer
  with voice processing for barge-in) or fake.

Server contract: `internal/appchannel` (Go). Replies may end with
`BUTTONS: A | B` (shown as buttons; a tap answers that message), and drive
mode sends `voice: true` so the agent answers briefly, with details after
a `---` line (spoken part before it).
