#!/usr/bin/env bash
# Install a dedicated headless Firefox for Claude Code on an agent host
# (a machine that only runs agents), with the agent given full control:
#
#   - Firefox ESR, headless, in its own profile (~/.mozilla/firefox/claudezilla-agent),
#     run as the systemd user service claudezilla-agent-browser
#   - the extension sideloaded into that profile (ESR honours
#     xpinstall.signatures.required=false, so no Mozilla signing is needed)
#   - managed policy for the extension: evaluate on, normal (non-private)
#     windows so logins persist
#   - claudezilla-host and claudezilla-mcp (Go) in a versioned read-only directory
#   - MCP server "claudezilla" registered for Claude Code (user scope), started
#     on demand, with every tool allowed (permission rule "mcp__claudezilla")
#
# The profile is used by nothing else, so the agent never sees the user's
# desktop browser. That one is reached separately, through
# scripts/claudezilla-remote-bridge (MCP server "claudezilla-desktop").
#
# Usage: install/install-agent-host.sh [--no-permissions] [--no-mcp]
# Env:   FIREFOX_BIN (default: firefox-esr)
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

PROFILE_DIR="$HOME/.mozilla/firefox/claudezilla-agent"
MANAGED_DIR="$HOME/.mozilla/managed-storage"
UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
UNIT=claudezilla-agent-browser
SETTINGS="$HOME/.claude/settings.json"

DO_PERMS=1 DO_MCP=1
for arg in "$@"; do
  case "$arg" in
    --no-permissions) DO_PERMS=0 ;;
    --no-mcp) DO_MCP=0 ;;
    *) sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
  esac
done

[[ "$(uname -s)" == Linux ]] || { echo "Linux only"; exit 1; }
FIREFOX="$(command -v "${FIREFOX_BIN:-firefox-esr}" || true)"
[[ -n "$FIREFOX" ]] || { echo "Error: ${FIREFOX_BIN:-firefox-esr} not found. Install Firefox ESR from Mozilla's APT repo (not the snap)."; exit 1; }
case "$(readlink -f "$FIREFOX")" in /snap/*) echo "Error: $FIREFOX is the snap; it can't run unsigned extensions or native messaging reliably."; exit 1 ;; esac
[[ -n "${XDG_RUNTIME_DIR:-}" ]] || { echo "Error: XDG_RUNTIME_DIR is not set (needed for the socket)"; exit 1; }

build_binaries
echo "== Building XPI"
XPI="$(python3 -I "$SRC/scripts/build-xpi.py" | head -1)"
echo "   $XPI"
systemctl --user stop "$UNIT" 2>/dev/null || true
install_binaries
write_nm_manifest
echo "== Managed policy (agent-host mode)"
install -d -m 0755 "$MANAGED_DIR"
cat > "$MANAGED_DIR/$EXT_ID.json" <<JSON
{
  "name": "$EXT_ID",
  "description": "Agent host: the agent has full control of this dedicated headless browser",
  "type": "storage",
  "data": { "allowEvaluate": true, "privateMode": false }
}
JSON
chmod 0644 "$MANAGED_DIR/$EXT_ID.json"

echo "== Profile $PROFILE_DIR"
install -d -m 0700 "$PROFILE_DIR" "$PROFILE_DIR/extensions"
rm -f "$PROFILE_DIR"/extensions/*.xpi   # dedicated profile: only this extension
install -m 0600 "$XPI" "$PROFILE_DIR/extensions/$EXT_ID.xpi"
cat > "$PROFILE_DIR/user.js" <<'USERJS'
// Claudezilla agent-host profile (written by install-agent-host.sh; re-run it to change)
user_pref("xpinstall.signatures.required", false);      // our own unsigned build (ESR only)
user_pref("extensions.autoDisableScopes", 0);           // enable the sideloaded XPI
user_pref("extensions.enabledScopes", 15);
user_pref("extensions.update.enabled", false);
user_pref("extensions.update.autoUpdateDefault", false);
user_pref("app.update.enabled", false);                 // apt updates Firefox
user_pref("browser.shell.checkDefaultBrowser", false);
user_pref("browser.aboutwelcome.enabled", false);
user_pref("browser.startup.homepage_override.mstone", "ignore");
user_pref("startup.homepage_welcome_url", "");
user_pref("browser.startup.page", 0);
user_pref("browser.sessionstore.resume_from_crash", false);
user_pref("datareporting.policy.dataSubmissionEnabled", false);
user_pref("datareporting.healthreport.uploadEnabled", false);
user_pref("toolkit.telemetry.enabled", false);
user_pref("toolkit.telemetry.unified", false);
user_pref("app.shield.optoutstudies.enabled", false);
user_pref("app.normandy.enabled", false);
user_pref("browser.tabs.warnOnClose", false);
user_pref("dom.disable_open_during_load", false);       // the agent is the user here
USERJS
# Firefox caches add-on state; make it rescan so a new XPI is picked up.
rm -f "$PROFILE_DIR"/{extensions.json,addonStartup.json.lz4,compatibility.ini}

echo "== systemd user service $UNIT"
install -d -m 0755 "$UNIT_DIR"
cat > "$UNIT_DIR/$UNIT.service" <<UNITEOF
[Unit]
Description=Headless Firefox ESR for Claude Code (Claudezilla agent host)

[Service]
Type=simple
ExecStart=$FIREFOX --headless --no-remote --profile $PROFILE_DIR
Environment=MOZ_HEADLESS_WIDTH=1440 MOZ_HEADLESS_HEIGHT=900 CLAUDEZILLA_ALLOW_QUIT=1
# A clean quit (closing the windows) saves the session; SIGTERM alone doesn't.
ExecStop=$MCP_BIN call quitBrowser
TimeoutStopSec=15
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
UNITEOF
systemctl --user daemon-reload
systemctl --user enable --now "$UNIT"

if (( DO_MCP )); then
  echo "== MCP server 'claudezilla' (Claude Code, user scope)"
  claude mcp remove --scope user claudezilla >/dev/null 2>&1 || true
  claude mcp add --scope user claudezilla \
    -e "CLAUDEZILLA_AUTOSTART_CMD=[\"systemctl\",\"--user\",\"start\",\"$UNIT\"]" \
    -- "$MCP_BIN"
  install_skill
fi

if (( DO_PERMS )); then
  echo "== Allowing every claudezilla tool in $SETTINGS"
  # One server-level rule covers every tool, including firefox_evaluate.
  add_permissions "$SETTINGS" mcp__claudezilla
fi

echo "== Waiting for the extension to connect"
for _ in $(seq 1 30); do
  [[ -S "$XDG_RUNTIME_DIR/claudezilla.sock" ]] && break
  sleep 1
done
if [[ -S "$XDG_RUNTIME_DIR/claudezilla.sock" ]]; then
  echo "   Connected: $XDG_RUNTIME_DIR/claudezilla.sock"
else
  echo "   [WARN] No socket after 30 s. Check: journalctl --user -u $UNIT"
fi
run_post_install_hooks

cat <<MSG

Done. New Claude Code sessions get the claudezilla tools with no prompts.
Browser: systemctl --user {status,restart} $UNIT
MSG
