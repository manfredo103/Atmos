#!/usr/bin/env bash
# Runs ON THE SERVER as the deploy user (called by GitHub Actions).
# Switches the "current" symlink to the new release, restarts the service,
# waits for the health check and rolls back automatically if it fails.
#
# Usage: remote-deploy.sh <release-name> [port]
set -euo pipefail

APP_DIR=/opt/atmos
SERVICE=atmos
RELEASE="${1:?release name required}"
PORT="${2:-8080}"
KEEP_RELEASES=5

NEW_DIR="$APP_DIR/releases/$RELEASE"
[[ -x "$NEW_DIR/atmos" ]] || chmod 755 "$NEW_DIR/atmos"

switch_to() {
  ln -sfn "$1" "$APP_DIR/current.tmp"
  mv -Tf "$APP_DIR/current.tmp" "$APP_DIR/current"
}

healthy() {
  for _ in $(seq 1 20); do
    if curl -fsS --max-time 2 "http://127.0.0.1:$PORT/api/health" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

PREVIOUS="$(readlink -f "$APP_DIR/current" 2>/dev/null || true)"

echo "Activating release $RELEASE"
switch_to "$NEW_DIR"
sudo systemctl restart "$SERVICE"

if healthy; then
  echo "Release $RELEASE is live and healthy."
  # Keep the newest releases, delete older ones (never the active one).
  ACTIVE="$(readlink -f "$APP_DIR/current")"
  ls -1dt "$APP_DIR"/releases/*/ | tail -n +"$((KEEP_RELEASES + 1))" | while read -r dir; do
    [[ "$(readlink -f "$dir")" == "$ACTIVE" ]] || rm -rf "$dir"
  done
  exit 0
fi

echo "Health check FAILED for release $RELEASE. Recent logs:"
journalctl -u "$SERVICE" -n 40 --no-pager || true

if [[ -n "$PREVIOUS" && -d "$PREVIOUS" && "$PREVIOUS" != "$NEW_DIR" ]]; then
  echo "Rolling back to $(basename "$PREVIOUS")"
  switch_to "$PREVIOUS"
  sudo systemctl restart "$SERVICE"
  if healthy; then
    echo "Rollback successful."
  else
    echo "Rollback is also unhealthy, check the server manually."
  fi
fi
exit 1
