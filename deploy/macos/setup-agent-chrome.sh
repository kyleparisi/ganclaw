#!/bin/bash
# Sets up an "agent Chrome" on a Mac for ganclaw's `chrome` MCP server:
#   - a separate Chrome profile with remote debugging on 127.0.0.1:9333
#     (Chrome refuses remote debugging on your everyday profile)
#   - an SSH reverse tunnel so the ganclaw server reaches it on
#     127.0.0.1:<remote_port> (default 9223)
# Both run as launchd agents and restart automatically.
#
# Usage: ./setup-agent-chrome.sh user@ganclaw-server [remote_port] [remote_ssh_port|none]
# Ports given on an earlier run are remembered; pass "none" to drop the SSH
# forward. The script prints an authorized_keys line for the server and starts
# the tunnel once you have added it. Then log in to the sites your agents
# need in the Chrome window that opens.
set -euo pipefail

SERVER="${1:?usage: $0 user@server [remote_port] [remote_ssh_port|none]}"
STATE="$HOME/Library/Application Support/ganclaw"
CONF="$STATE/tunnel.conf"
REMOTE_PORT="" REMOTE_SSH_PORT=""
# shellcheck source=/dev/null
[ -f "$CONF" ] && . "$CONF"
REMOTE_PORT="${2:-${REMOTE_PORT:-9223}}"
# Optional: carry Mac Remote Login over the same SSH connection.
REMOTE_SSH_PORT="${3:-$REMOTE_SSH_PORT}"
[ "$REMOTE_SSH_PORT" = none ] && REMOTE_SSH_PORT=""

valid_port() { [[ $1 =~ ^[1-9][0-9]{3,4}$ ]] && (( $1 <= 65535 )); }
valid_port "$REMOTE_PORT" || { echo "remote_port must be between 1024 and 65535" >&2; exit 1; }
SSH_FORWARD_XML=""
SSH_PERMIT_LISTEN=""
if [ -n "$REMOTE_SSH_PORT" ]; then
  if ! valid_port "$REMOTE_SSH_PORT" || [ "$REMOTE_SSH_PORT" = "$REMOTE_PORT" ]; then
    echo "remote_ssh_port must be a distinct port between 1024 and 65535" >&2
    exit 1
  fi
  SSH_FORWARD_XML="<string>-R</string><string>127.0.0.1:$REMOTE_SSH_PORT:127.0.0.1:22</string>"
  SSH_PERMIT_LISTEN=",permitlisten=\"127.0.0.1:$REMOTE_SSH_PORT\""
fi
# Not Chrome's usual 9222: other tools (or another Chrome) often hold it, and
# Chrome then silently listens on [::1] instead of 127.0.0.1.
LOCAL_PORT="${GANCLAW_CHROME_PORT:-9333}"
CHROME="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
PROFILE="$HOME/Library/Application Support/ganclaw-agent-chrome"
# launchd jobs may be denied access to ~/.ssh by macOS privacy controls
# ("Load key ...: Operation not permitted"), so the tunnel's key and
# known_hosts live under Application Support instead (STATE, above).
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
printf 'REMOTE_PORT=%s\nREMOTE_SSH_PORT=%s\n' "$REMOTE_PORT" "$REMOTE_SSH_PORT" > "$CONF"

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
    $SSH_FORWARD_XML
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

start_agent() {
  local label=$1 plist="$AGENTS/$1.plist"
  plutil -lint -s "$plist"
  launchctl bootout "gui/$UID/$label" 2>/dev/null || true
  # bootout returns before the job (and e.g. Chrome) has fully exited;
  # bootstrapping too early fails with "5: Input/output error".
  for _ in $(seq 1 40); do
    launchctl print "gui/$UID/$label" >/dev/null 2>&1 || break
    sleep 0.5
  done
  launchctl bootstrap "gui/$UID" "$plist"
}

start_agent dev.ganclaw.agent-chrome

cat <<EOF

On the server, put this line in the ssh user's ~/.ssh/authorized_keys,
replacing any earlier ganclaw-chrome-tunnel line. It allows only these
reverse forwards, no shell:

restrict,port-forwarding,permitlisten="127.0.0.1:$REMOTE_PORT"$SSH_PERMIT_LISTEN,command="/bin/false" $(cat "$KEY.pub")

EOF
# The tunnel exits if the server refuses any forward, so start it only once
# the server accepts them all.
if [ -t 0 ]; then read -r -p "Press Enter once the line is on the server... " _; fi
start_agent dev.ganclaw.chrome-tunnel

cat <<EOF

Agent Chrome and tunnel are running (launchd: dev.ganclaw.agent-chrome,
dev.ganclaw.chrome-tunnel; logs in $LOGS). In the Chrome window that
opened, log in to the sites your agents need.
EOF

if [ -n "$REMOTE_SSH_PORT" ]; then
  nc -z 127.0.0.1 22 2>/dev/null || echo "
Warning: nothing listens on 127.0.0.1:22. Enable Remote Login (below)."
  cat <<EOF

Mac command access is forwarded to server port $REMOTE_SSH_PORT. It exposes
this Mac's SSH login to the server, so firewall that port to the ganclaw
service user like the Chrome port (see README.md).

1. Enable Remote Login in System Settings > General > Sharing.
2. Install a separate server-to-Mac SSH key, then from the server run:

   ssh -p $REMOTE_SSH_PORT <mac-user>@127.0.0.1
EOF
fi
