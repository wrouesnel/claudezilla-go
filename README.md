# claudezilla-go

> **A diverging fork.** claudezilla-go started from [Claudezilla](https://github.com/boot-industries/claudezilla)
> v0.7.7 by Chris Lyons / boot.industries, and has since gone its own way: the native host and MCP
> server are rewritten in Go, and the design is aimed at agents that run on a different machine
> from your browser. It isn't intended to merge back. For the original, see
> **[boot-industries/claudezilla](https://github.com/boot-industries/claudezilla)**.

**Firefox browser automation for Claude Code**, built for selective, seamless remote control: agents
on another machine drive your Firefox only while you let them, or get a headless browser of their own.

## What's new in this fork

- **Go host and MCP server.** Two static binaries, `claudezilla-host` and `claudezilla-mcp`, with no
  Node.js, no npm and no third-party Go modules. Each agent's MCP server starts in about 10 ms and
  uses about 4 MB of memory, against about 0.5 s and 75 MB for the Node.js original.
- **Selective remote control.** Claude Code on an agent host drives your desktop Firefox through one
  forwarded socket, only while the forward runs. Stopping it takes control back at once. OpenSSH
  is the default transport; any other plugs in through a config hook.
- **A browser of the agent's own.** `install/install-agent-host.sh` sets up a dedicated headless
  Firefox ESR with full agent control: evaluate on, no prompts, logins kept in that profile. It
  starts on demand and quits cleanly.
- **Two MCP servers side by side.** `claudezilla` is the local or headless browser. `claudezilla-desktop`
  is your desktop, reached remotely, with a more cautious permission set.
- **Settings an administrator can fix.** Firefox managed storage can preset and lock evaluate and
  private mode.
- **Takeover protection.** The fork has its own extension ID and native host name, so an update to
  upstream's listing can never replace it. There's no `update_url` in the source, and the XPI build
  is reproducible.
- **No outbound traffic.** Fonts, the donation page and the Stripe handling are removed, and the CSP
  sets `connect-src 'none'`.
- **Tighter trust boundaries:**
  - Every command carries the MCP server's own agent ID, and tab ownership is always checked.
  - Only you can turn private mode off. Agents can ask for more privacy, never less.
  - Password fields are refused and `file:` URLs are blocked.
  - Network capture covers only managed tabs, with sensitive headers dropped and secrets redacted.
  - Focus loops are capped at 50 iterations.
  - The socket and token never sit in a shared temp directory.
- **Fewer failed first calls.** A command sent just after opening or navigating a page waits for the
  page, instead of failing while it loads.
- **An audit of upstream v0.7.7.** The published add-on matches its source, with no signs of
  takeover. See [HARDENING.md](HARDENING.md).

## Install

Needs Firefox 140+ from a deb or tarball (Snap and Flatpak builds restrict native messaging), plus
Go 1.23+ to build. Set `CLAUDEZILLA_BIN_DIR` to use prebuilt binaries instead.

**Agent host: a headless browser with full control**

```sh
install/install-agent-host.sh
```

This needs Firefox ESR (for example from Mozilla's APT repo), which runs this unsigned build. The
script builds the binaries and XPI and creates the profile `~/.mozilla/firefox/claudezilla-agent`
and the systemd user unit `claudezilla-agent-browser`. It also registers the MCP server `claudezilla`
with Claude Code and allows all of its tools.

**Your desktop**

```sh
install/install-desktop-host.sh          # native host only (for remote agents)
install/install-desktop-host.sh --mcp    # also Claude Code on this machine
```

Then install the XPI. Release Firefox needs it signed by Mozilla: `make sign` does that through
your addons.mozilla.org API key (`AMO_JWT_ISSUER`, `AMO_JWT_SECRET`), as an unlisted add-on that
isn't published. Open the signed file in Firefox to install it. ESR, Developer Edition and Nightly
can run the unsigned `make xpi` build with `xpinstall.signatures.required=false`.

Coming from upstream Claudezilla? `install/uninstall-upstream.sh --dry-run` shows what it would
remove (its native host manifest, the Node MCP registration, the extra permission rules, the skill
link and the headless unit). Run it without `--dry-run`, then remove the old extension in
about:addons.

**Remote control of your desktop from the agent host**

```sh
mkdir -p ~/.config/claudezilla
echo 'CLAUDEZILLA_REMOTE_HOST=me@desktop' > ~/.config/claudezilla/remote.env
cp install/systemd/claudezilla-bridge.service ~/.config/systemd/user/   # adjust ExecStart's path
systemctl --user enable --now claudezilla-bridge    # socket forward + token refresh
scripts/claudezilla-remote-bridge mcp-add           # registers "claudezilla-desktop"
```

See [HARDENING.md](HARDENING.md) for the full layout, the security model and the trade-offs.

## Permissions

On the desktop, `install/claude-permissions.json` allows every tool except `firefox_evaluate`,
`firefox_start_loop` and `firefox_set_config`, which keep prompting. Evaluate also needs the opt-in
in the popup: **Claudezilla → Security → Allow JavaScript evaluation**. On the agent host,
`mcp__claudezilla` allows everything.

## Environment variables

| Variable | Read by | Purpose |
|----------|---------|---------|
| `CLAUDEZILLA_IPC_DIR` | host and MCP server | Directory for the socket, auth token and debug log (made `0700`). Both processes need the same value: the host inherits Firefox's environment. The default is `$XDG_RUNTIME_DIR`, then `~/.claudezilla`, never a shared temp directory. |
| `CLAUDEZILLA_AUTOSTART_CMD` | MCP server | Starts the browser when the host isn't reachable, as a JSON argv array or a whitespace-split string. It never goes through a shell. Listing tools never starts it; the first tool call does. |
| `CLAUDEZILLA_AUTOSTART_WAIT_MS` / `_RETRY_COOLDOWN_MS` / `_LAUNCH_TIMEOUT_MS` | MCP server | How long to wait for the socket (45 s), how long before retrying a failed start (60 s), and how long the start command may run (20 s). |
| `CLAUDEZILLA_TOOL_MODE` | MCP server | `lazy` lists only `firefox_activate` until a category is activated (same as `--tools lazy`). |
| `CLAUDEZILLA_SCREENSHOT_FILES` | MCP server | `1` also writes screenshots to `claudezilla-screenshots/` in the temp directory, for harnesses that can't show images. |
| `CLAUDEZILLA_OUTPUT_DIR` | MCP server | Write every file (screenshot copies and `savePath`) here. `savePath` must then be a file name in this directory. For running the MCP server on the browser's machine on behalf of a remote agent, where a client copies the directory back. |
| `CLAUDEZILLA_OUTPUT_DIR_REPORTED` | MCP server | How tool results name that directory (default: the same path), such as where the agent will find the copies. |
| `CLAUDEZILLA_AGENT_TIMEOUT_MS` | MCP server | Close an agent's tabs after this long without a call (default 10 minutes; `0` disables). |
| `CLAUDEZILLA_ALLOW_QUIT` | host (from Firefox's environment) | Allows `quitBrowser`. Set it only for a browser that exists for agents. |

`claudezilla-mcp call <command> ['<json params>']` sends one host command and prints the reply, for
scripts and supervisors. For example, `claudezilla-mcp call quitBrowser` closes every window
cleanly, which `SIGTERM` doesn't. The agent-host unit uses it as `ExecStop`.

## Tools

There are 34 tools, all listed by default.

- **Browser:**
  - `firefox_create_window` opens a tab in the shared 12-tab pool.
  - `firefox_navigate`, `firefox_get_tabs`, `firefox_close_tab` and `firefox_close_window` manage tabs.
  - `firefox_list_all_tabs`, `firefox_attach_tab` and `firefox_detach_tab` work with your own tabs.
  - `firefox_resize_window` and `firefox_set_viewport` set the window size.
- **Reading:**
  - `firefox_get_accessibility_snapshot` is a text tree with `[ref_N]` handles.
  - `firefox_find` locates elements by text.
  - `firefox_get_content` returns page text in pages.
  - `firefox_get_page_state` and `firefox_diff_page_state` give and compare page structure.
  - `firefox_get_element` and `firefox_wait_for` inspect an element or wait for a condition.
- **Acting:** `firefox_click`, `firefox_type`, `firefox_press_key` and `firefox_scroll`, by `ref` or
  selector. `firefox_screenshot` is sized for vision models and supports `region` and `savePath`.
- **DevTools:**
  - `firefox_get_console` returns uncaught errors.
  - `firefox_get_network` lists the managed tabs' requests, redacted.
  - `firefox_evaluate` runs JavaScript in the page (opt-in).
- **Coordination:** `firefox_request_tab_space`, `firefox_grant_tab_space` and `firefox_get_slot_requests`.
- **Config and consent:** `firefox_set_config` sets a per-agent domain allow-list and consent
  handling. `firefox_handle_consent` dismisses consent dialogs.
- **Focus loops:** `firefox_start_loop`, `firefox_stop_loop` and `firefox_loop_status`.
- **Diagnostics:** `firefox_diagnose`.

## Architecture

```
Claude Code ⇄ claudezilla-mcp ⇄ Unix socket (+ token) ⇄ claudezilla-host ⇄ native messaging ⇄ extension
   one MCP server per session        local, or forwarded       started by Firefox
```

The socket protocol is one JSON line per request, carrying a bearer token that the host
regenerates every time it starts. Forwarding that socket is all remote control takes.

## Development

```sh
make          # bin/claudezilla-host, bin/claudezilla-mcp
make test     # go vet + go test, then node --test (extension and black-box MCP tests)
make xpi      # reproducible dist/claudezilla-go@wrouesnel.github.io-<ver>.unsigned.xpi
make dist     # static binaries for linux/darwin (amd64, arm64) and windows/amd64
```

The tool definitions are data, in `internal/mcp/catalog/` (`tools.json`, `instructions.txt`). The
extension is still upstream's JavaScript, with the changes listed above. Windows binaries build, but
there's no Windows installer yet.

## License and credits

MIT. Claudezilla is © Chris Lyons / boot.industries; the fork's changes are under the same license.
