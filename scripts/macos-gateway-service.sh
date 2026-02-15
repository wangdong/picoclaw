#!/usr/bin/env bash
set -euo pipefail

LABEL="com.picoclaw.gateway"
PLIST_PATH="${HOME}/Library/LaunchAgents/${LABEL}.plist"
LOG_DIR="${HOME}/.picoclaw/logs"
OUT_LOG="${LOG_DIR}/gateway.out.log"
ERR_LOG="${LOG_DIR}/gateway.err.log"
DEFAULT_BIN="${HOME}/.local/bin/picoclaw"
GUI_DOMAIN="gui/$(id -u)"

usage() {
  cat <<USAGE
Usage:
  $0 install [--binary /absolute/path/to/picoclaw]
  $0 uninstall
  $0 start
  $0 stop
  $0 restart
  $0 status
  $0 logs
USAGE
}

resolve_binary() {
  local candidate="${1:-}"

  if [[ -n "$candidate" ]]; then
    if [[ "$candidate" != /* ]]; then
      echo "Binary path must be absolute: $candidate" >&2
      exit 1
    fi
    if [[ ! -x "$candidate" ]]; then
      echo "Binary not executable: $candidate" >&2
      exit 1
    fi
    echo "$candidate"
    return
  fi

  if [[ -x "$DEFAULT_BIN" ]]; then
    echo "$DEFAULT_BIN"
    return
  fi

  if command -v picoclaw >/dev/null 2>&1; then
    command -v picoclaw
    return
  fi

  echo "Could not find picoclaw binary. Pass --binary /abs/path/to/picoclaw" >&2
  exit 1
}

is_loaded() {
  launchctl print "${GUI_DOMAIN}/${LABEL}" >/dev/null 2>&1
}

write_plist() {
  local bin_path="$1"

  mkdir -p "$(dirname "$PLIST_PATH")" "$LOG_DIR"

  cat >"$PLIST_PATH" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>${LABEL}</string>

  <key>ProgramArguments</key>
  <array>
    <string>${bin_path}</string>
    <string>gateway</string>
  </array>

  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin:${HOME}/.local/bin</string>
  </dict>

  <key>WorkingDirectory</key>
  <string>${HOME}</string>

  <key>RunAtLoad</key>
  <true/>

  <key>KeepAlive</key>
  <true/>

  <key>ThrottleInterval</key>
  <integer>10</integer>

  <key>ProcessType</key>
  <string>Background</string>

  <key>StandardOutPath</key>
  <string>${OUT_LOG}</string>

  <key>StandardErrorPath</key>
  <string>${ERR_LOG}</string>
</dict>
</plist>
PLIST
}

cmd_install() {
  local bin_path=""

  if [[ "${1:-}" == "--binary" ]]; then
    bin_path="$(resolve_binary "${2:-}")"
  else
    bin_path="$(resolve_binary "")"
  fi

  write_plist "$bin_path"

  if is_loaded; then
    launchctl bootout "$GUI_DOMAIN" "$PLIST_PATH" || true
  fi

  launchctl bootstrap "$GUI_DOMAIN" "$PLIST_PATH"
  launchctl kickstart -k "${GUI_DOMAIN}/${LABEL}"

  echo "Installed and started: ${LABEL}"
  echo "plist: $PLIST_PATH"
  echo "stdout: $OUT_LOG"
  echo "stderr: $ERR_LOG"
}

cmd_uninstall() {
  if is_loaded; then
    launchctl bootout "$GUI_DOMAIN" "$PLIST_PATH" || true
  fi
  rm -f "$PLIST_PATH"
  echo "Uninstalled: ${LABEL}"
}

cmd_start() {
  if is_loaded; then
    launchctl kickstart -k "${GUI_DOMAIN}/${LABEL}"
  else
    if [[ ! -f "$PLIST_PATH" ]]; then
      echo "Service plist not found: $PLIST_PATH" >&2
      echo "Run: $0 install" >&2
      exit 1
    fi
    launchctl bootstrap "$GUI_DOMAIN" "$PLIST_PATH"
  fi
  echo "Started: ${LABEL}"
}

cmd_stop() {
  if is_loaded; then
    launchctl bootout "$GUI_DOMAIN" "$PLIST_PATH"
    echo "Stopped: ${LABEL}"
  else
    echo "Service is not running: ${LABEL}"
  fi
}

cmd_restart() {
  if is_loaded; then
    launchctl kickstart -k "${GUI_DOMAIN}/${LABEL}"
  else
    if [[ ! -f "$PLIST_PATH" ]]; then
      echo "Service plist not found: $PLIST_PATH" >&2
      echo "Run: $0 install" >&2
      exit 1
    fi
    launchctl bootstrap "$GUI_DOMAIN" "$PLIST_PATH"
  fi
  echo "Restarted: ${LABEL}"
}

cmd_status() {
  if is_loaded; then
    echo "Service loaded: ${LABEL}"
    launchctl print "${GUI_DOMAIN}/${LABEL}" | sed -n '1,40p'
  else
    echo "Service not loaded: ${LABEL}"
    if [[ -f "$PLIST_PATH" ]]; then
      echo "plist exists: $PLIST_PATH"
    else
      echo "plist missing: $PLIST_PATH"
    fi
  fi

  [[ -f "$OUT_LOG" ]] && echo "stdout log: $OUT_LOG"
  [[ -f "$ERR_LOG" ]] && echo "stderr log: $ERR_LOG"
}

cmd_logs() {
  mkdir -p "$LOG_DIR"
  touch "$OUT_LOG" "$ERR_LOG"
  echo "Tailing logs..."
  echo "  $OUT_LOG"
  echo "  $ERR_LOG"
  tail -f "$OUT_LOG" "$ERR_LOG"
}

main() {
  local action="${1:-}"
  case "$action" in
    install)
      cmd_install "${2:-}" "${3:-}"
      ;;
    uninstall)
      cmd_uninstall
      ;;
    start)
      cmd_start
      ;;
    stop)
      cmd_stop
      ;;
    restart)
      cmd_restart
      ;;
    status)
      cmd_status
      ;;
    logs)
      cmd_logs
      ;;
    *)
      usage
      exit 1
      ;;
  esac
}

main "$@"
