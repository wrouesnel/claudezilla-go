# claudezilla-go: design, audit and security model

This is a fork of [boot-industries/claudezilla](https://github.com/boot-industries/claudezilla)
v0.7.7 (upstream commit `5e4a237`), released as **`claudezilla-go@wrouesnel.github.io`
v0.7.7.3**. README.md gives the overview; SECURITY.md is upstream's. This file describes what's
different and why.

Upstream assumes Claude Code and Firefox run on the same computer. This fork is aimed at running
Claude Code agents on a **separate agent host** and giving them a browser in two ways:

- **Your own Firefox, selectively.** An agent drives your desktop browser only while you've let it
  in. Handing over is meant to be seamless: no reinstalling, no per-click prompts for routine tools.
  The control path is a single forwarded socket, so stopping the forward takes control back at once.
  While it runs, the agent can't touch your other tabs unless you attach one, can't leave private
  windows if you've turned them on, can't read `file:` URLs, and can't type into password fields.
- **A browser of its own, fully.** The agent host gets a dedicated headless Firefox where the agent
  has complete control: evaluate is on, no prompts, logins persist. Nothing of yours is in it.

## Audit (2026-11-12)

Scope: everything shipped or executed — `extension/`, `host/`, `mcp/` and its dependency tree,
installers, `plugin/` (Claude Code hooks/skill), CI, git history.

- **The published AMO build matches the source.** `claudezilla-0.7.7.xpi` from AMO
  (sha256 `c4f5b6fc…9007be`) is byte-identical to `extension/` at `5e4a237`, apart from the Mozilla
  signature files and one trailing newline in `manifest.json`.
- **No malicious or takeover indicators.** There is no telemetry, obfuscation, remote code,
  `update_url` or kill switch.
  - The only outbound requests were Google Fonts and an opt-in Stripe donation POST.
  - The upstream maintainer wrote 339 of the 360 commits. Recent security changes all tighten things.
  - CI doesn't publish anything, so a takeover would have to go through the maintainer's AMO or
    GitHub account.
- **Design weaknesses** (fixed here unless noted):
  - `evaluate` runs with extension privileges, and its blocklist can be bypassed. Kept: it's useful,
    and it stays opt-in.
  - The background script accepted privileged messages from any content-script context.
  - Tab ownership was optional and agent IDs could be spoofed.
  - An agent could turn off private mode by itself.
  - Network capture covered the whole browser and kept `Set-Cookie`.
  - An auto-allowed, unlimited loop tool plus the Stop hook could keep re-running an injected prompt.
  - The socket, token and log could fall back to `/tmp`.
  - The installers ran `npm install` without using the lockfile.
- **Dependencies:** the Node.js MCP server pulled in 95 npm packages (the MCP SDK with an HTTP
  server stack, OAuth, ajv and zod) for a stdio server that uses none of them. The fork replaced
  the host and MCP server with Go, using the standard library only, so `go.mod` has no requirements.

## Changes from upstream

| Area | Change |
|---|---|
| Takeover protection | New gecko ID, so AMO updates for `claudezilla@boot.industries` can never replace this build. No `update_url` in the source; `scripts/build-xpi.py --update-url` adds one for self-hosted updates. The native host is renamed `claudezilla_go` and accepts only this extension ID. |
| No outbound traffic | Donation page, Stripe handling, support links and Google Fonts are removed. The CSP has `connect-src 'none'`. The welcome page opens once instead of on every popup open. |
| `firefox_evaluate` | **As upstream**: off until enabled in the popup, and not in the auto-allow list, so each call prompts in Claude Code. It runs with extension privileges, and its blocklist isn't a real boundary. |
| Runtime messages | Content scripts may only send `openPopup`. Only our own extension pages may query status or stop a loop. The legacy navigate, click, type and screenshot handlers are gone. |
| Private mode | Set only by you, in the popup (**Agent tabs → Open in private windows**). Off by default in this fork, so agent tabs work in your own session with your logins; upstream defaulted to on. `firefox_set_private_mode` is gone. An agent can ask for *more* privacy but never less. Firefox's "Run in private windows" permission is separate: it's needed only when agent tabs are private. If a private window can't be opened, the call fails instead of silently opening a normal window. |
| Ownership | MCP always injects its own `agentId`. The extension requires one for every tab command and only resolves pool tabs the caller owns, or tabs you've attached. Other agents' IDs are truncated. |
| Navigation | `file:` URLs are blocked, since the agent may be on another machine. |
| Network capture | Only managed tabs are recorded. Sensitive response headers are dropped. URL fragments and more OAuth/session parameters are redacted. |
| Typing | Password fields are refused. You sign in yourself. |
| Loops | Default 10 iterations, hard cap 50. `firefox_start_loop` and `firefox_set_config` are no longer auto-allowed. The Stop hook trusts only a socket and token we own, never one in `/tmp`. |
| IPC | No fallback to a shared temp dir. Screenshots to `savePath` are created with `O_EXCL`. |
| Managed policy | Firefox managed storage (`~/.mozilla/managed-storage/claudezilla-go@wrouesnel.github.io.json`) can fix `allowEvaluate` and `privateMode` (the popup shows them locked). Only the agent-host installer writes it. Without it, nothing changes. |
| Remote use | `scripts/claudezilla-remote-bridge` forwards the socket and keeps the token current (OpenSSH by default, any transport via a config hook). `install/install-desktop-host.sh` installs only the native host. `install/install-agent-host.sh` sets up the dedicated headless browser. |
| Go host and MCP server | `claudezilla-host` and `claudezilla-mcp` replace the Node.js `host/` and `mcp/`. They use the same socket protocol and produce byte-identical `tools/list` and `initialize` output (the catalog is data in `internal/mcp/catalog/`), with no third-party modules. Per agent: about 10 ms startup and 4 MB of memory, against about 0.5 s and 75 MB. |
| Supply chain | No npm dependencies at runtime. `scripts/build-xpi.py` makes a reproducible XPI. Binaries are installed read-only in `~/.local/share/claudezilla-go/<ver>/bin`, with a `current` symlink. |
| Removed | `worker/`, `website/`, `marketing/`, `wireframes/`, the stale 0.5.9 zip, upstream CI. |

Not changed, so be aware of it:

- The socket token is a plain bearer token. Whoever holds it can drive every managed tab and list
  your tabs' URLs. Keep the forwarded socket and copied token in a 0700 directory.
- `firefox_attach_tab` and `firefox_list_all_tabs` don't ask for confirmation. Attaching lets an
  agent act in one of your own logged-in tabs.

## Two modes

| | Agent host's own browser (`claudezilla`) | Your desktop (`claudezilla-desktop`) |
|---|---|---|
| Browser | Firefox ESR, `--headless`, dedicated profile `~/.mozilla/firefox/claudezilla-agent`, systemd user unit `claudezilla-agent-browser`, started on demand by the MCP server | Your own Firefox |
| Policy | Managed: evaluate on, normal windows (logins persist in that profile). Firefox never lets extensions open `file:` URLs, so serve local files over `http://localhost` | Popup settings: normal windows, in a Claudezilla tab group in your active window |
| Claude Code permissions | `mcp__claudezilla`: every tool, no prompts | Explicit allow-list; evaluate, loops and config prompt |
| Who can connect | Local Claude Code sessions (socket + token in `$XDG_RUNTIME_DIR`, 0600) | Only while the socket forward runs |
| Install | `install/install-agent-host.sh` | `install/install-desktop-host.sh` + `scripts/claudezilla-remote-bridge` |

## Layout (remote)

```
Browser host (your Firefox)                          Agent host
  extension (signed XPI)                               Claude Code
    └ native messaging → claudezilla-host                └ claudezilla-mcp  (CLAUDEZILLA_IPC_DIR=…/claudezilla-remote)
        (~/.local/share/claudezilla-go/<ver>/bin)            ├ claudezilla.sock  ← socket forward ← browser-host socket
        ├ $XDG_RUNTIME_DIR/claudezilla.sock                  └ claudezilla-auth.token ← token refresh ← browser-host token
        └ $XDG_RUNTIME_DIR/claudezilla-auth.token
```

## Build and release

```sh
python3 -I scripts/build-xpi.py [--update-url https://…/updates.json]   # dist/<id>-<ver>.unsigned.xpi + sha256
make test                                                               # go test + node --test
```

Release Firefox only installs Mozilla-signed XPIs. Either sign the XPI unlisted through the AMO API
(it isn't published, Mozilla only signs it), or use ESR, Developer Edition or Nightly with
`xpinstall.signatures.required=false`.

## Install

Agent host's own headless browser (needs a non-snap Firefox ESR, e.g. from Mozilla's APT repo):

```sh
install/install-agent-host.sh   # builds the XPI, profile, service, MCP server and permissions
```

Your desktop: `install/install-desktop-host.sh`, then install the signed XPI. On the agent host:

```sh
mkdir -p ~/.config/claudezilla
echo 'CLAUDEZILLA_REMOTE_HOST=me@desktop' > ~/.config/claudezilla/remote.env
cp install/systemd/claudezilla-bridge.service ~/.config/systemd/user/   # adjust the path if needed
systemctl --user enable --now claudezilla-bridge   # socket forward + token refresh
scripts/claudezilla-remote-bridge mcp-add          # registers "claudezilla-desktop" for Claude Code
scripts/claudezilla-remote-bridge status
```

To use another transport, define `bridge_forward` (bind `$SOCK` to the remote socket, stay in the
foreground) and `bridge_token` (pipe the remote token into `store_token`) in `remote.env`.

## Updating from upstream

```sh
git fetch upstream && git log --oneline HEAD..upstream/main
git diff HEAD...upstream/main -- extension host mcp install plugin   # review before merging
```

After merging, review any new network calls, permissions and message handlers in the extension.
Port any host or MCP changes to Go (tool definitions are JSON in
`internal/mcp/catalog/`), bump the fourth version component in `extension/manifest.json` and
`internal/version`, and rebuild.
