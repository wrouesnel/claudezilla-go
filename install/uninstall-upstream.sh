#!/usr/bin/env bash
# Remove an upstream Claudezilla (boot-industries/claudezilla) install, before
# or after installing claudezilla-go. Linux and macOS. It touches only what
# upstream's installer created, and says what it left alone:
#
#   - native messaging manifest claudezilla.json (only if it allows claudezilla@boot.industries)
#   - Claude Code MCP server "claudezilla" (only if it runs a Node mcp/server.js)
#   - permission rules for tools claudezilla-go doesn't have or keeps prompting
#     (firefox_set_private_mode, firefox_start_loop, firefox_set_config)
#   - the claudezilla-browser skill symlink, if it points into a Claudezilla checkout
#   - the disabled claudezilla-browser systemd user unit
#   - with --remove-profile: the claudezilla-headless Firefox profile
#
# The extension itself is in your Firefox profile; remove it in about:addons
# (Firefox must not be edited while it runs). The checkout is left in place.
#
# Usage: install/uninstall-upstream.sh [--remove-profile] [--dry-run]
set -euo pipefail

REMOVE_PROFILE=0 DRY=0
for arg in "$@"; do
  case "$arg" in
    --remove-profile) REMOVE_PROFILE=1 ;;
    --dry-run) DRY=1 ;;
    *) sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
  esac
done
# act "what" cmd...: run cmd (quietly) and report, or in --dry-run just say it.
act() { local what="$1"; shift; if (( DRY )); then echo "   would: $what"; else "$@" >/dev/null 2>&1 || true; echo "   done: $what"; fi; }

case "$(uname -s)" in
  Linux)  NM_DIR="$HOME/.mozilla/native-messaging-hosts"; FF_DIR="$HOME/.mozilla/firefox" ;;
  Darwin) NM_DIR="$HOME/Library/Application Support/Mozilla/NativeMessagingHosts"; FF_DIR="$HOME/Library/Application Support/Firefox" ;;
  *) echo "Unsupported OS"; exit 1 ;;
esac

echo "== Native messaging manifest"
M="$NM_DIR/claudezilla.json"
if [[ -f "$M" ]] && grep -q 'claudezilla@boot.industries' "$M"; then
  CHECKOUT_HOST="$(sed -n 's/.*"path": *"\([^"]*\)".*/\1/p' "$M")"
  act "remove $M (host was $CHECKOUT_HOST)" rm -f "$M"
else
  echo "   none"
fi

echo "== Claude Code MCP server 'claudezilla'"
if command -v claude >/dev/null; then
  CFG="$(claude mcp get claudezilla 2>/dev/null || true)"
  if grep -q 'mcp/server.js' <<<"$CFG"; then
    act "remove it (it runs the Node server)" claude mcp remove --scope user claudezilla
  elif [[ -n "$CFG" ]]; then
    echo "   left alone: it isn't upstream's Node server"
  else
    echo "   none"
  fi

  echo "== Permission rules"
  SETTINGS="$HOME/.claude/settings.json"
  if [[ -f "$SETTINGS" ]]; then
    DRY="$DRY" python3 -I - "$SETTINGS" <<'PY'
import json, os, sys, tempfile
path = sys.argv[1]
data = json.load(open(path))
allow = data.get("permissions", {}).get("allow", [])
drop = {f"mcp__claudezilla__firefox_{t}" for t in ("set_private_mode", "start_loop", "set_config")}
gone = [r for r in allow if r in drop]
if not gone:
    print("   nothing to remove")
    sys.exit()
if os.environ.get("DRY") == "1":
    print("   would: remove " + ", ".join(gone))
    sys.exit()
print("   done: remove " + ", ".join(gone))
data["permissions"]["allow"] = [r for r in allow if r not in drop]
fd, tmp = tempfile.mkstemp(dir=os.path.dirname(path))
with os.fdopen(fd, "w") as f:
    json.dump(data, f, indent=2); f.write("\n")
os.chmod(tmp, os.stat(path).st_mode & 0o777)
os.replace(tmp, path)
PY
  fi
fi

echo "== Skill symlink"
for L in "$HOME/.claude/skills/claudezilla-browser" "$HOME/.agents/skills/claudezilla-browser"; do
  if [[ -L "$L" ]] && [[ "$(readlink "$L")" == */plugin/skills/claudezilla-browser ]]; then
    act "remove $L -> $(readlink "$L")" rm -f "$L"
  fi
done

echo "== systemd user unit claudezilla-browser"
U="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/claudezilla-browser.service"
if [[ -f "$U" ]]; then
  export XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"
  act "disable and remove $U" sh -c 'systemctl --user disable --now claudezilla-browser; rm -f "$1"; systemctl --user daemon-reload' _ "$U"
else
  echo "   none"
fi

echo "== Headless profile"
P="$FF_DIR/claudezilla-headless"
if [[ -d "$P" ]]; then
  if (( REMOVE_PROFILE )); then
    act "remove $P (its profiles.ini entry is harmless; drop it in Firefox's profile manager)" rm -r "$P"
  else
    echo "   left $P (pass --remove-profile to delete it)"
  fi
else
  echo "   none"
fi

cat <<MSG

Left for you:
  - The extension: about:addons → Claudezilla → Remove.
  - The checkout (e.g. ${CHECKOUT_HOST:+$(dirname "$(dirname "$CHECKOUT_HOST")")}): delete it when you no longer want it.
MSG
