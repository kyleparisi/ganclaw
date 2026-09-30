# Agent Chrome on a Mac

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

## Mac

```sh
./setup-agent-chrome.sh ganclaw-user@your-server
```

It creates a separate Chrome profile (Chrome refuses remote debugging on your
everyday profile), an SSH key used only for the tunnel, and two launchd
agents that restart Chrome and the tunnel automatically. Log in to the sites
your agents need in the Chrome window it opens.

## Server

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
