# Shared by the claudezilla-go installers (sourced; bash).
#
# Binaries are installed to a versioned, read-only directory:
#   ${XDG_DATA_HOME:-~/.local/share}/claudezilla-go/<version>/bin/{claudezilla-host,claudezilla-mcp}
# with a "current" symlink, so later edits to the checkout never change what
# Firefox or Claude Code run, and upgrades don't need re-registration.

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EXT_ID="claudezilla-go@wrouesnel.github.io"
NM_NAME="claudezilla_go"
VERSION="$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' "$SRC/extension/manifest.json" | head -1)"
case "$(uname -s)" in
  Linux)  NM_DIR="$HOME/.mozilla/native-messaging-hosts"; DATA="${XDG_DATA_HOME:-$HOME/.local/share}" ;;
  Darwin) NM_DIR="$HOME/Library/Application Support/Mozilla/NativeMessagingHosts"; DATA="$HOME/Library/Application Support" ;;
  *) echo "Unsupported OS: $(uname -s)"; exit 1 ;;
esac
BASE="$DATA/claudezilla-go"
DEST="$BASE/$VERSION"
CURRENT="$BASE/current"

# Build the binaries (needs Go), or use prebuilt ones in $CLAUDEZILLA_BIN_DIR.
build_binaries() {
  BIN_SRC="${CLAUDEZILLA_BIN_DIR:-}"
  if [[ -z "$BIN_SRC" ]]; then
    command -v go >/dev/null || { echo "Error: Go not found. Install Go, or set CLAUDEZILLA_BIN_DIR to prebuilt binaries."; exit 1; }
    BIN_SRC="$SRC/bin"
    echo "== Building claudezilla-host and claudezilla-mcp ($(go version | awk '{print $3}'))"
    (cd "$SRC" && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$BIN_SRC/" ./cmd/...)
  fi
  for b in claudezilla-host claudezilla-mcp; do
    [[ -x "$BIN_SRC/$b" ]] || { echo "Error: $BIN_SRC/$b missing"; exit 1; }
  done
}

install_binaries() {
  echo "== Installing binaries to $DEST (read-only)"
  if [[ -d "$DEST" ]]; then chmod -R u+w "$DEST"; rm -rf "$DEST"; fi
  install -d -m 0755 "$DEST/bin"
  install -m 0555 "$BIN_SRC/claudezilla-host" "$BIN_SRC/claudezilla-mcp" "$DEST/bin/"
  chmod 0555 "$DEST/bin" "$DEST"
  ln -sfn "$VERSION" "$CURRENT"
  HOST_BIN="$CURRENT/bin/claudezilla-host"
  MCP_BIN="$CURRENT/bin/claudezilla-mcp"
}

write_nm_manifest() {
  echo "== Native messaging manifest"
  install -d -m 0755 "$NM_DIR"
  cat > "$NM_DIR/$NM_NAME.json" <<JSON
{
  "name": "$NM_NAME",
  "description": "claudezilla-go browser automation bridge",
  "path": "$HOST_BIN",
  "type": "stdio",
  "allowed_extensions": ["$EXT_ID"]
}
JSON
  chmod 0644 "$NM_DIR/$NM_NAME.json"
}

# add_permissions <settings.json> <rule>...: add allow rules, dropping any
# older per-tool rules for the same server.
add_permissions() {
  python3 -I - "$@" <<'PY'
import json, os, sys, tempfile
path, rules = sys.argv[1], sys.argv[2:]
data = json.load(open(path)) if os.path.exists(path) else {}
allow = data.setdefault("permissions", {}).setdefault("allow", [])
for rule in rules:
    if rule not in allow:
        allow.append(rule)
os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
fd, tmp = tempfile.mkstemp(dir=os.path.dirname(path) or ".")
with os.fdopen(fd, "w") as f:
    json.dump(data, f, indent=2); f.write("\n")
if os.path.exists(path):
    os.chmod(tmp, os.stat(path).st_mode & 0o777)
os.replace(tmp, path)
PY
}
