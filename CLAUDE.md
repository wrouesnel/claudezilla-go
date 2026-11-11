# claudezilla-go

A diverging fork of [Claudezilla](https://github.com/boot-industries/claudezilla) v0.7.7: Firefox
browser automation for Claude Code, aimed at agents that run on a different machine from the
browser. README.md is the overview; HARDENING.md has the audit, security model and install layout.

## Layout

```
extension/            Firefox WebExtension (MV2, JavaScript; upstream's with the fork's changes)
cmd/claudezilla-host  native messaging host, started by Firefox (Go)
cmd/claudezilla-mcp   MCP server over stdio, one process per agent; also `call <command>` (Go)
internal/ipc          socket/token/log paths and timeout rules (shared by both binaries)
internal/client       socket client, on-demand browser start, idle-agent reaper
internal/mcp          JSON-RPC/MCP server; catalog/ holds tools.json, gateway.json, instructions.txt
internal/nativemsg    4-byte LE length + JSON framing
install/              install-agent-host.sh (headless ESR, full control), install-desktop-host.sh, lib.sh
scripts/              build-xpi.py (reproducible XPI), claudezilla-remote-bridge (socket forward)
plugin/               Claude Code plugin: Stop hook for focus loops (bash + nc + jq), skill
tests/                node --test: extension unit tests, and black-box tests of bin/claudezilla-mcp
```

No third-party Go modules, and no npm dependencies at runtime. Keep it that way: add a dependency
only with a strong reason.

## Build and test

```sh
make test    # go vet, go test ./..., then node --test (needs bin/ from `make`)
make xpi     # dist/claudezilla-go@wrouesnel.github.io-<ver>.unsigned.xpi
```

The version lives in `extension/manifest.json`, `internal/version/version.go`, `package.json` and
the popup label; `TestVersionMatchesManifest` checks the first two. Bump the fourth component for
each release.

## Identity

- Extension ID `claudezilla-go@wrouesnel.github.io`; native messaging host `claudezilla_go`
  (manifest `~/.mozilla/native-messaging-hosts/claudezilla_go.json`, which must list that ID in
  `allowed_extensions`).
- No `update_url` in the source manifest; `scripts/build-xpi.py --update-url` injects one.

## Protocol and invariants

- MCP server ⇄ host: Unix socket `claudezilla.sock`, one JSON line per request
  `{command, params, authToken}`, one JSON line back. The token (`claudezilla-auth.token`, 0600) is
  regenerated each time the host starts; clients re-read it on every request.
- IPC directory: `CLAUDEZILLA_IPC_DIR`, else `$XDG_RUNTIME_DIR`, else `~/.claudezilla` (0700). Never
  a shared temp directory. The host inherits Firefox's environment; the MCP server uses its own.
- The MCP server always overwrites `agentId` with its own; the extension requires it for every tab
  command and checks pool-tab ownership.
- Host → extension messages ≤ 1 MB; extension → host and socket lines ≤ 10 MB.
- Results from the extension are passed to the agent as raw JSON (key order preserved), not
  re-marshalled through maps.
- `tools/list` and `initialize` output must stay identical to the catalog files; edit tools there.
- `quitBrowser` only works when the browser's own environment has `CLAUDEZILLA_ALLOW_QUIT=1`; no
  MCP tool exposes it.
- Agent-host policy comes from Firefox managed storage
  (`~/.mozilla/managed-storage/claudezilla-go@wrouesnel.github.io.json`): `allowEvaluate`,
  `privateMode`. Without that file the extension behaves as on a desktop.

## Remote use

`scripts/claudezilla-remote-bridge forward` binds `$XDG_RUNTIME_DIR/claudezilla-remote/claudezilla.sock`
to the browser host's socket and keeps the token current (OpenSSH by default; `bridge_forward` and
`bridge_token` in `~/.config/claudezilla/remote.env` replace the transport). `mcp-add` registers it
as the MCP server `claudezilla-desktop`.
