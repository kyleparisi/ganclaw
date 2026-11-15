# Connecting a Mac

`setup-mac.sh` connects a Mac to the ganclaw server over one SSH reverse
tunnel. It always carries an agent Chrome, and can also carry the Mac's SSH
login ([below](#add-mac-command-access-to-the-same-tunnel)).

## Agent Chrome

Some agent work needs a browser that is logged in to your accounts (for
example Instagram), from your own network rather than a datacenter IP.
ganclaw's `chrome` MCP server can drive a Chrome running on a Mac through an
SSH reverse tunnel:

```
Mac:     agent Chrome (own profile, remote debugging on 127.0.0.1:9333)
            │ ssh -R 127.0.0.1:9223 → 127.0.0.1:9333   (launchd keeps it up)
server:  127.0.0.1:9223  ← firewalled to the ganclaw service user
            │
         chrome MCP server (Playwright over CDP) → agents
```

### Mac

```sh
./setup-mac.sh ganclaw-user@your-server
```

It creates a separate Chrome profile (Chrome refuses remote debugging on your
everyday profile), an SSH key used only for the tunnel, and two launchd
agents that restart Chrome and the tunnel automatically. It prints an
`authorized_keys` line for the server (below) and waits for you to add it
before starting the tunnel. Log in to the sites your agents need in the
Chrome window it opens.

Rerunning the script keeps the profile, key and ports from the last run.

### Server

1. Add the `authorized_keys` line the script prints to the service user's
   `~/.ssh/authorized_keys`. It permits only the reverse tunnel.
2. Restrict the tunnel port to the service user, or any local user could
   drive your logged-in browser. With ufw, add before the
   `-A ufw-before-output -o lo -j ACCEPT` line in `/etc/ufw/before.rules`:

   ```
   -A ufw-before-output -o lo -p tcp --dport 9223 -m owner ! --uid-owner ganclaw -j REJECT --reject-with tcp-reset
   ```

   then `sudo ufw reload`.
3. Add the MCP server to `ganclaw.toml`:

   ```toml
   [[mcp_servers]]
   name = "chrome"
   command = "/usr/local/lib/ganclaw/node/bin/node"
   args = ["/usr/local/lib/ganclaw/playwright-mcp/node_modules/@playwright/mcp/cli.js", "--cdp-endpoint", "http://127.0.0.1:9223"]
   description = "Your own Chrome, logged in to your accounts. Use only for tasks that need those logins; offline when the Mac is off."
   ```

When the Mac is off, the `chrome` tools fail fast with a clear error and
agents report it.

## Add Mac command access to the same tunnel

Pass a third argument to forward the Mac's SSH service alongside Chrome:

```sh
./setup-mac.sh ganclaw-user@your-server 9223 2223
```

When it pauses, replace the earlier ganclaw tunnel line in the
server's `authorized_keys` with the one it prints, which also permits the SSH
port. Later reruns keep the SSH forward; pass `none` as the third argument to
remove it.

On the Mac, enable **System Settings > General > Sharing > Remote Login** and
allow only the intended Mac user. The tunnel key only authenticates the Mac to
the server; command access requires a separate key in the other direction.
Generate that key as the ganclaw service user on the server, and install its
public key in the Mac user's `~/.ssh/authorized_keys`. Keep host key checking
enabled and verify the Mac host key fingerprint during the initial connection.

Port 2223 exposes the Mac's SSH login to everyone on the server, so restrict
it to the service user with the same firewall rule as the Chrome port,
replacing 9223 with 2223. Then, as that user:

```sh
ssh -i /path/to/mac_access_key -p 2223 mac-user@127.0.0.1 'sw_vers'
scp -i /path/to/mac_access_key -P 2223 ./input.txt mac-user@127.0.0.1:~/input.txt
```

Agents with shell execution can invoke SSH for commands, builds, and transfers;
no additional MCP server is needed for those operations. Xcode, signing, and
repository setup still happen on the Mac. Both Chrome and command access share
one outbound tunnel connection, and both go offline if it disconnects or if
the server refuses either port (for example because something else holds
it); `~/Library/Logs/ganclaw/tunnel.log` then shows
`remote port forwarding failed`. These
launch agents run in the logged-in user's session; keep the Mac awake for
unattended work.

Desktop control additionally needs a Mac automation helper with the necessary
macOS permissions. This script does not install one. Once selected, its local
port can be added as another `-R` forward on the same connection, with a matching
`permitlisten` restriction and server firewall rule.
