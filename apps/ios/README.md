# Ganclaw for iOS

A private messenger for your ganclaw agents: one thread per agent, live
streaming replies, attachments, reply buttons for approvals, copy-ready
blocks, scheduled-job runs and a hands-free drive mode.

It talks to ganclaw's app channel over HTTPS, with a WebSocket for live
updates.

## Set up the server

Add to `ganclaw.toml` and restart ganclaw:

```toml
[app]
listen = "127.0.0.1:8443"                   # behind a TLS reverse proxy
public_url = "https://ganclaw.example.com"  # how the phone reaches it
# Or terminate TLS in ganclaw itself:
# listen = "0.0.0.0:8443"
# tls_cert = "/etc/ganclaw/tls/fullchain.pem"
# tls_key  = "/etc/ganclaw/tls/privkey.pem"
```

Plain HTTP is only accepted on a loopback address, so device tokens never
cross the network unencrypted.

## Pair the phone

```bash
ganclaw app pair --name "Kyle's iPhone"
```

Enter the server and the code in the app, or open the printed
`ganclaw://pair?...` link on the phone. Codes work once and expire after
10 minutes. `ganclaw app devices` lists phones; `ganclaw app unpair <id>`
revokes one.

## Scheduled jobs in the app

Deliver a scheduled run to an agent's thread, named by its session:

```bash
ganclaw run --agent infra --to app:infra --session nightly-log-check --quiet \
  "Check last night's logs. Reply NO_REPLY if nothing is wrong."
```

The run shows up under Jobs, and "Ask about this run" replies to it in the
agent's thread. `ganclaw send --to app:infra "text"` posts a plain message.

## Reply buttons

An agent can end a reply with a line like

```
BUTTONS: Approve | Not now
```

The app shows the labels as buttons (up to four). Tapping one sends the
label as a reply to that message, so the agent knows what was approved.
Mention this in an agent's `TOOLS.md` to have it ask that way.

## Building

See [CLAUDE.md](CLAUDE.md): `apps/ios/test/run.sh` builds the app, runs the
unit tests and drives the app in a simulator through its debug control
server.

### Voice settings

Open the gear on the inbox, then **Settings → Voice**. Tapping a voice
selects it and plays a sample; voices in your language are listed, Premium
and Enhanced first. The selection and speaking speed are saved on this
device and used for drive-mode replies. If a selected voice is removed,
speech falls back to the system default. The better voices have to be
downloaded first: Settings › Accessibility › Spoken Content › Voices.
Speech plays even with the ring/silent switch on silent.
