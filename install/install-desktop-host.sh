#!/usr/bin/env bash
# Install ONLY the Claudezilla native-messaging host on the computer that runs
# Firefox (Linux or macOS), for use with Claude Code on another host through
# scripts/claudezilla-remote-bridge. Unlike install-linux.sh it does not touch
# Claude Code settings, MCP registrations, Firefox profiles or systemd.
#
# The host is copied to a versioned, read-only directory, so later changes to
# this checkout (git pull, edits) never change the code Firefox runs.
set -euo pipefail

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' "$SRC/host/package.json" | head -1)"
case "$(uname -s)" in
  Linux)  NM_DIR="$HOME/.mozilla/native-messaging-hosts"; DATA="${XDG_DATA_HOME:-$HOME/.local/share}" ;;
  Darwin) NM_DIR="$HOME/Library/Application Support/Mozilla/NativeMessagingHosts"; DATA="$HOME/Library/Application Support" ;;
  *) echo "Unsupported OS: $(uname -s) (use install-windows.ps1)"; exit 1 ;;
esac
DEST="$DATA/claudezilla-hardened/$VERSION"

NODE="$(command -v node || true)"
[[ -n "$NODE" ]] || { echo "Error: node not found in PATH (Node.js >= 18.17 required)"; exit 1; }

if [[ -d "$DEST" ]]; then chmod -R u+w "$DEST"; rm -rf "$DEST"; fi
install -d -m 0755 "$DEST/host"
install -m 0444 "$SRC"/host/{index,ipc,protocol,debug-log,cli}.js "$SRC/host/package.json" "$DEST/host/"
cat > "$DEST/host/run.sh" <<RUN
#!/bin/sh
exec "$NODE" "$DEST/host/index.js" "\$@"
RUN
chmod 0555 "$DEST/host/run.sh"
chmod 0555 "$DEST/host" "$DEST"

install -d -m 0755 "$NM_DIR"
cat > "$NM_DIR/claudezilla_hardened.json" <<JSON
{
  "name": "claudezilla_hardened",
  "description": "Claudezilla (hardened) browser automation bridge",
  "path": "$DEST/host/run.sh",
  "type": "stdio",
  "allowed_extensions": ["claudezilla-hardened@wrouesnel.github.io"]
}
JSON
chmod 0644 "$NM_DIR/claudezilla_hardened.json"

cat <<MSG
Installed Claudezilla host $VERSION to $DEST
Native messaging manifest: $NM_DIR/claudezilla_hardened.json

Next:
  1. Install the signed claudezilla-hardened@wrouesnel.github.io XPI in Firefox and restart it.
  2. When the extension connects, the host creates:
       \${XDG_RUNTIME_DIR:-~/.claudezilla}/claudezilla.sock
       \${XDG_RUNTIME_DIR:-~/.claudezilla}/claudezilla-auth.token
     On the agent host, start scripts/claudezilla-remote-bridge forward (it reaches this socket and token).
Note: Snap and Flatpak Firefox restrict native messaging; use a deb/tarball Firefox.
MSG
