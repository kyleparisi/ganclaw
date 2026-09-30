# ganclaw

A small, self-hosted chat gateway for coding agents. ganclaw connects
Telegram bots to agents that run as local CLI subprocesses —
[Codex](https://github.com/openai/codex) and
[Claude Code](https://docs.claude.com/en/docs/claude-code) — using each CLI's
own subscription login. No API keys are needed.

One Go binary, one SQLite file, one long-lived `codex app-server` shared by
every agent. Idle, it uses about 20 MB.

## Features

- **Agents on subscription logins.** Codex is driven over its app-server
  JSON-RPC protocol; Claude Code (`claude -p`) is the fallback. When a
  provider hits a usage or rate limit, ganclaw skips it until the limit
  resets (it reads the exact reset time) and uses the next one.
- **Telegram bots**, each bound to an agent and restricted to an allowlist of
  users. Replies stream into the chat. Photos, documents, albums and voice
  notes are passed to the agent; voice notes can be transcribed locally with
  [whisper.cpp](https://github.com/ggml-org/whisper.cpp).
- **Commands:** `/new` (or `/reset`) to clear the conversation, `/stop`, `/status`, `/help`, shown in Telegram's
  command menu.
- **Conversations** persist per chat (the CLIs keep the history; ganclaw maps
  chats to their session IDs). Messages in a chat run one at a time.
- **Agent tools via MCP.** Any stdio MCP server (for example a Playwright
  browser) is given to every agent, and ganclaw adds its own tools so agents
  can message people (`send_message`), ask another agent (`ask_agent`), hand
  work to one (`hand_off`) and see who's available (`list_agents`).
- **A local API and CLI** for schedulers and scripts (cron, systemd timers,
  [Dagu](https://github.com/dagu-org/dagu), …): send a message or run an agent,
  optionally delivering its reply to a chat. Idempotency keys make retries
  safe.
- **Health checks and alerts** that use no AI, including when the gateway
  itself is down.
- **OpenClaw compatibility**: scripts that call `openclaw message send` /
  `openclaw agent` / `openclaw system event` can point at ganclaw unchanged.

## How it works

```
Telegram ─┐                                     ┌─ codex app-server (JSON-RPC, one process)
          ├─ router (one turn per chat) ─ chain ─┤
local API ┘        │                             └─ claude -p (per turn)
 (Unix socket)   SQLite                               │
                 sessions                     MCP servers (browser, ganclaw tools, …)
```

Each agent is a working directory (its *workspace*) plus instruction files
from that directory (`IDENTITY.md`, `SOUL.md`, `USER.md`, `AGENTS.md`,
`TOOLS.md`, `MEMORY.md` by default) that become its system prompt.

## Requirements

- Linux (the systemd units and Codex's sandbox assume it; development works on
  macOS).
- Go 1.27+ to build.
- The `codex` CLI and/or the `claude` CLI, logged in with your subscription.
- Codex's sandbox needs [bubblewrap](https://github.com/containers/bubblewrap)
  (`apt install bubblewrap`) unless agents run with `sandbox = "danger-full-access"`.
- Optional: `ffmpeg` and whisper.cpp's `whisper-cli` for voice notes; Node.js
  and `@playwright/mcp` for a browser.

## Quick start

```sh
go build -o ganclaw ./cmd/ganclaw
cp ganclaw.example.toml ganclaw.toml   # edit it
```

Log the CLIs in, each with its own state directory so the bots don't share
your personal setup:

```sh
CODEX_HOME=/var/lib/ganclaw/codex codex login --device-auth
claude setup-token        # prints a long-lived token for CLAUDE_CODE_OAUTH_TOKEN
```

Create a bot with [@BotFather](https://t.me/BotFather), then:

```sh
export GANCLAW_TELEGRAM_ASSISTANT_TOKEN=...   # named by token_env in the config
export CLAUDE_CODE_OAUTH_TOKEN=...
./ganclaw health -config ganclaw.toml         # check logins and limits
./ganclaw serve -config ganclaw.toml
```

Message your bot. If you're not in `allow_users` yet, ganclaw logs your
Telegram user ID when it ignores you.

## Configuration

See [`ganclaw.example.toml`](ganclaw.example.toml) for every option. The main
sections:

| Section | Purpose |
|---|---|
| top level | `state_dir`, provider order, turn timeout, attachment retention, API socket |
| `[codex]`, `[claude]` | binaries, state directories, default model, sandbox / permission mode, web search |
| `[[agents]]` | name, workspace, instruction files, and optional per-agent `codex_model`, `sandbox`, `claude_model`, `permission_mode` |
| `[[telegram]]` | bot name, agent, `token_env` (the environment variable holding its token), `allow_users` |
| `[[contacts]]` | names for people, so scripts say `--to alex` instead of chat IDs |
| `[[mcp_servers]]` | MCP servers given to every agent, with an optional `description` added to agents' instructions |
| `[transcribe]` | whisper.cpp voice transcription (off unless configured) |
| `[monitor]` | who to alert, stale-bot threshold, reminder interval, HTTP probes |
| `[openclaw_compat]` | fallback to the real OpenClaw CLI, default agent and notify contact for system events |

Secrets never go in the config file: each bot names the environment variable
that holds its token.

## CLI

```
ganclaw serve   -config ganclaw.toml          run the gateway
ganclaw health  -config ganclaw.toml          check provider logins and limits directly
ganclaw check   -config ganclaw.toml          check a running gateway and alert (for a timer)

ganclaw send --to alex "Disk is 90% full"                     plain message, no AI
ganclaw run  --agent support "Summarise today's tickets"      run an agent, print its reply
ganclaw run  --agent support --to alex --prompt-file task.md  run and deliver the reply to a chat
ganclaw status                                                provider availability
```

Client commands find the gateway through `-socket`, `$GANCLAW_SOCKET`, or the
`api_socket` in the config. Exit codes are meant for schedulers:

| Code | Meaning | Retry? |
|---|---|---|
| 0 | ok | — |
| 2 | usage error | no |
| 3 | every provider is unavailable | later |
| 4 | unknown contact, bot or agent, or a bad request | no |
| 5 | timed out | maybe |
| 6 | busy (queue full, or the same idempotency key is running) | soon |
| 7 | gateway not reachable | later |

`run` options: `--session NAME` continues a named conversation (runs without
`--to` are otherwise one-off), `--timeout 10m`, `--idempotency-key KEY`,
`--json`.

## Local API

HTTP + JSON on a Unix socket (default `<state_dir>/ganclaw.sock`). The socket
is `0600` in a `0700` directory, and connections from users other than the
service user and root are refused (checked with `SO_PEERCRED`).

| Endpoint | Purpose |
|---|---|
| `POST /v1/send` | `{to, via?, text, idempotency_key?}` |
| `POST /v1/run` | `{agent, prompt, to?, via?, session?, timeout_seconds?, quiet?, heartbeat?, async?, idempotency_key?}` |
| `GET /v1/status` | provider availability and limits |
| `GET /v1/agents` | agents, their bots, and contacts |
| `GET /v1/health` | providers, bot polling state and probe results |

Errors are `{"ok": false, "error": {"code", "message"}}` with stable codes
(`unknown_contact`, `unavailable`, `busy`, …).

## Browser and other MCP servers

Add stdio MCP servers under `[[mcp_servers]]`; ganclaw passes them to Codex
(as `-c mcp_servers.*` settings) and Claude (a generated `--mcp-config` whose
tools are pre-allowed). A headless browser with
[Playwright MCP](https://github.com/microsoft/playwright-mcp):

```toml
[[mcp_servers]]
name = "browser"
command = "node"
args = ["/path/to/@playwright/mcp/cli.js", "--headless", "--isolated"]
description = "Headless browser, logged out. Use for public pages."
```

To let agents use a browser that is logged in to your accounts, from your own
network, see [`deploy/macos/`](deploy/macos/): it runs a separate Chrome
profile on a Mac and reaches it over a restricted SSH reverse tunnel.

## Running as a service

[`deploy/`](deploy/) has hardened systemd units:

- `ganclaw.service` runs the gateway as a dedicated user, reading secrets from
  a root-only `EnvironmentFile`. ganclaw strips its own `GANCLAW_*` variables
  before starting providers, so agents never see bot tokens.
- `ganclaw-check.service` + `.timer` run `ganclaw check` every 5 minutes as
  root, and when the gateway fails. Alerts go through the gateway, or straight
  to Telegram if the gateway is down: once when a problem starts, a reminder
  every few hours, and once when it clears.

## Security model

Agents can run commands, so the real boundary is the operating system:

- Run ganclaw as a dedicated, unprivileged user. Agents can do what that user
  can do.
- The systemd unit makes the system read-only, limits writes to the service
  user's home, blocks privilege escalation and caps memory and CPU.
- Codex's sandbox and Claude's permission modes add limits inside that; the
  defaults are cautious (`workspace-write`, `acceptEdits`). Loosen them per
  agent if you'd rather rely on the user account alone, and keep them strict
  for agents that read untrusted content such as email — that is where prompt
  injection comes from.
- Bots only answer users in `allow_users`, in private chats.
- Anything in `[[mcp_servers]]` command lines appears in the process list:
  keep secrets out of it.

## Migrating from OpenClaw

`ganclaw openclaw-compat` (or the binary invoked as `openclaw`, e.g. through a
symlink) accepts OpenClaw's `message send`, `agent` and `system event`
commands and their JSON output. Bots and agents ganclaw doesn't know yet are
handed to the real OpenClaw CLI (`[openclaw_compat] fallback_bin`), so you can
point every script at ganclaw first and move bots over one at a time: copy a
bot's token, add its `[[agents]]` and `[[telegram]]` entries, remove it from
OpenClaw, restart both.

## Development

```sh
go test -race ./...
git config core.hooksPath .githooks   # optional: scan commits for secrets with gitleaks
```

Tests don't call real services: providers run against in-memory fakes, and
the store uses a temporary SQLite database. `internal/transcribe` has an
integration test that runs real `ffmpeg` and `whisper-cli` when
`GANCLAW_TEST_WHISPER_BIN`, `GANCLAW_TEST_WHISPER_MODEL` and
`GANCLAW_TEST_AUDIO` are set.

The Codex app-server protocol schema is vendored in
`internal/provider/codex/schema` (regenerate with
`codex app-server generate-json-schema --out <dir>`).

## Roadmap

- Slack
- Webhook endpoints that start agent runs (with signature checks)
- An optional TCP listener with bearer-token auth for remote triggers
- Group chats

## License

[MIT](LICENSE)
