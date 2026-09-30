#!/bin/bash
# Sets up an "agent Chrome" on a Mac for ganclaw's `chrome` MCP server:
#   - a separate Chrome profile with remote debugging on 127.0.0.1:9333
#     (Chrome refuses remote debugging on your everyday profile)
#   - an SSH reverse tunnel so the ganclaw server reaches it on
#     127.0.0.1:<remote_port> (default 9223)
# Both run as launchd agents and restart automatically.
#
# Usage: ./setup-agent-chrome.sh user@ganclaw-server [remote_port]
# Then add the printed authorized_keys line on the server, and log in to the
# sites your agents need in the Chrome window that opens.
set -euo pipefail

SERVER="${1:?usage: $0 user@server [remote_port]}"
REMOTE_PORT="${2:-9223}"
# Not Chrome's usual 9222: other tools (or another Chrome) often hold it, and
# Chrome then silently listens on [::1] instead of 127.0.0.1.
LOCAL_PORT="${GANCLAW_CHROME_PORT:-9333}"
CHROME="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
PROFILE="$HOME/Library/Application Support/ganclaw-agent-chrome"
# launchd jobs may be denied access to ~/.ssh by macOS privacy controls
# ("Load key ...: Operation not permitted"), so the tunnel's key and
# known_hosts live under Application Support instead.
STATE="$HOME/Library/Application Support/ganclaw"
KEY="$STATE/chrome_tunnel_key"
KNOWN_HOSTS="$STATE/known_hosts"
OLD_KEY="$HOME/.ssh/ganclaw_chrome_tunnel"
AGENTS="$HOME/Library/LaunchAgents"
LOGS="$HOME/Library/Logs/ganclaw"

[ -x "$CHROME" ] || { echo "Google Chrome not found at $CHROME" >&2; exit 1; }
mkdir -p "$PROFILE" "$AGENTS" "$LOGS" "$STATE"
chmod 700 "$STATE"
if [ ! -f "$KEY" ] && [ -f "$OLD_KEY" ]; then
  # Keep the key from an earlier run so the server's authorized_keys still matches.
  cp -p "$OLD_KEY" "$KEY" && cp -p "$OLD_KEY.pub" "$KEY.pub" && rm -f "$OLD_KEY" "$OLD_KEY.pub"
fi
[ -f "$KEY" ] || ssh-keygen -q -t ed25519 -N "" -C "ganclaw-chrome-tunnel" -f "$KEY"
chmod 600 "$KEY"

cat > "$AGENTS/dev.ganclaw.agent-chrome.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>dev.ganclaw.agent-chrome</string>
  <key>ProgramArguments</key>
  <array>
    <string>$CHROME</string>
    <string>--user-data-dir=$PROFILE</string>
    <string>--remote-debugging-port=$LOCAL_PORT</string>
    <string>--no-first-run</string>
    <string>--no-default-browser-check</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>30</integer>
  <key>StandardErrorPath</key><string>$LOGS/agent-chrome.log</string>
</dict>
</plist>
EOF

cat > "$AGENTS/dev.ganclaw.chrome-tunnel.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>dev.ganclaw.chrome-tunnel</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/bin/ssh</string>
    <string>-N</string>
    <string>-T</string>
    <string>-i</string><string>$KEY</string>
    <string>-o</string><string>IdentitiesOnly=yes</string>
    <string>-o</string><string>ExitOnForwardFailure=yes</string>
    <string>-o</string><string>ServerAliveInterval=30</string>
    <string>-o</string><string>ServerAliveCountMax=3</string>
    <string>-o</string><string>StrictHostKeyChecking=accept-new</string>
    <string>-o</string><string>UserKnownHostsFile=$KNOWN_HOSTS</string>
    <string>-R</string><string>127.0.0.1:$REMOTE_PORT:127.0.0.1:$LOCAL_PORT</string>
    <string>$SERVER</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>30</integer>
  <key>StandardErrorPath</key><string>$LOGS/chrome-tunnel.log</string>
</dict>
</plist>
EOF

# Stop the old agent Chrome first so its port is free, then make sure no
# other process (possibly another user's) holds the port.
launchctl bootout "gui/$UID/dev.ganclaw.agent-chrome" 2>/dev/null || true
for _ in $(seq 1 40); do
  launchctl print "gui/$UID/dev.ganclaw.agent-chrome" >/dev/null 2>&1 || break
  sleep 0.5
done
if nc -z 127.0.0.1 "$LOCAL_PORT" 2>/dev/null || nc -z ::1 "$LOCAL_PORT" 2>/dev/null; then
  echo "Port $LOCAL_PORT is already in use on this Mac (see: sudo lsof -nP -iTCP:$LOCAL_PORT -sTCP:LISTEN)." >&2
  echo "Pick another with GANCLAW_CHROME_PORT=<port> $0 $*" >&2
  exit 1
fi

for label in dev.ganclaw.agent-chrome dev.ganclaw.chrome-tunnel; do
  plist="$AGENTS/$label.plist"
  plutil -lint -s "$plist"
  launchctl bootout "gui/$UID/$label" 2>/dev/null || true
  # bootout returns before the job (and e.g. Chrome) has fully exited;
  # bootstrapping too early fails with "5: Input/output error".
  for _ in $(seq 1 40); do
    launchctl print "gui/$UID/$label" >/dev/null 2>&1 || break
    sleep 0.5
  done
  launchctl bootstrap "gui/$UID" "$plist"
done

cat <<EOF

Agent Chrome and tunnel are installed (launchd: dev.ganclaw.agent-chrome,
dev.ganclaw.chrome-tunnel; logs in $LOGS).

1. On the server, append this line to the ssh user's ~/.ssh/authorized_keys.
   It only allows this reverse tunnel: no shell, no other forwarding.

restrict,port-forwarding,permitlisten="127.0.0.1:$REMOTE_PORT",command="/bin/false" $(cat "$KEY.pub")

2. In the Chrome window that opened, log in to the sites your agents need.
EOF
