#!/usr/bin/env bash
# Install claudezilla-go on the computer that runs your Firefox (Linux/macOS):
# the native host, and optionally a local MCP server for Claude Code here.
#
#   install/install-desktop-host.sh          # host only: agents on another machine
#                                            # reach it through claudezilla-remote-bridge
#   install/install-desktop-host.sh --mcp    # also register "claudezilla" for Claude Code
#                                            # on this machine, with the cautious allow-list
#
# Then install the signed XPI in Firefox (see HARDENING.md).
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

WITH_MCP=0
for arg in "$@"; do
  case "$arg" in
    --mcp) WITH_MCP=1 ;;
    *) sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
  esac
done

build_binaries
install_binaries
write_nm_manifest

if (( WITH_MCP )); then
  echo "== MCP server 'claudezilla' (Claude Code, user scope)"
  claude mcp remove --scope user claudezilla >/dev/null 2>&1 || true
  claude mcp add --scope user claudezilla -- "$MCP_BIN"
  # Everything except evaluate, loops and config changes, which keep prompting.
  mapfile -t RULES < <(python3 -I -c 'import json,sys; print("\n".join(json.load(open(sys.argv[1]))["allow"]))' "$SRC/install/claude-permissions.json")
  add_permissions "$HOME/.claude/settings.json" "${RULES[@]}"
  install_skill
fi

run_post_install_hooks

cat <<MSG

Installed claudezilla-go $VERSION ($DEST)
Native messaging manifest: $NM_DIR/$NM_NAME.json

Next: install the signed $EXT_ID XPI in Firefox and restart it.
When the extension connects, the host creates claudezilla.sock and
claudezilla-auth.token in \${XDG_RUNTIME_DIR:-~/.claudezilla} (0600).
Note: Snap and Flatpak Firefox restrict native messaging; use a deb/tarball Firefox.
MSG
