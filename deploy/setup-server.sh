#!/usr/bin/env bash
# One-time server setup for Atmos on Ubuntu (e.g. Oracle Cloud VM).
#
# Run on the server with sudo, passing the PUBLIC deploy key:
#   sudo bash setup-server.sh "ssh-ed25519 AAAA... github-actions-atmos"
#
# What it does:
#   - creates a system user "atmos" (runs the app) and a user "deploy" (used by GitHub Actions)
#   - creates /opt/atmos with releases/ and a placeholder .env
#   - installs and enables the systemd service
#   - allows "deploy" to restart ONLY the atmos service via sudo
#   - opens ports 80/443 in the Ubuntu iptables firewall (Oracle images block them by default)
set -euo pipefail

APP_DIR=/opt/atmos
SERVICE_USER=atmos
DEPLOY_USER=deploy
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PUBKEY="${1:-}"

if [[ $EUID -ne 0 ]]; then
  echo "Please run with sudo." >&2
  exit 1
fi
if [[ -z "$PUBKEY" ]]; then
  echo "Usage: sudo bash setup-server.sh \"<deploy public key>\"" >&2
  exit 1
fi
if [[ ! -f "$SCRIPT_DIR/atmos.service" ]]; then
  echo "atmos.service must be in the same directory as this script." >&2
  exit 1
fi

echo "==> Installing packages"
apt-get update -qq
apt-get install -y -qq curl

echo "==> Creating users"
id -u "$SERVICE_USER" &>/dev/null || useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin "$SERVICE_USER"
id -u "$DEPLOY_USER" &>/dev/null || useradd --create-home --shell /bin/bash "$DEPLOY_USER"
usermod -aG systemd-journal "$DEPLOY_USER"   # lets the deploy script print logs on failure

echo "==> Authorizing deploy key"
DEPLOY_HOME="$(getent passwd "$DEPLOY_USER" | cut -d: -f6)"
install -d -m 700 -o "$DEPLOY_USER" -g "$DEPLOY_USER" "$DEPLOY_HOME/.ssh"
touch "$DEPLOY_HOME/.ssh/authorized_keys"
grep -qxF "$PUBKEY" "$DEPLOY_HOME/.ssh/authorized_keys" || echo "$PUBKEY" >> "$DEPLOY_HOME/.ssh/authorized_keys"
chown "$DEPLOY_USER:$DEPLOY_USER" "$DEPLOY_HOME/.ssh/authorized_keys"
chmod 600 "$DEPLOY_HOME/.ssh/authorized_keys"

echo "==> Creating $APP_DIR"
install -d -m 755 -o "$DEPLOY_USER" -g "$DEPLOY_USER" "$APP_DIR" "$APP_DIR/releases"

if [[ ! -f "$APP_DIR/.env" ]]; then
  cat > "$APP_DIR/.env" <<'EOF'
# Atmos production configuration. Edit with: sudo nano /opt/atmos/.env
INFLUXDB_URL=http://localhost:8086
INFLUXDB_TOKEN=CHANGE_ME
INFLUXDB_ORG=Atmos
INFLUXDB_BUCKET=sensor_data
PORT=8080
APP_NAME=Atmos
VOLLEYBALL_URL=
GRAFANA_URL=
GRAFANA_EMBED_URL=
EOF
fi
chown root:"$SERVICE_USER" "$APP_DIR/.env"
chmod 640 "$APP_DIR/.env"

echo "==> Installing systemd service"
install -m 644 "$SCRIPT_DIR/atmos.service" /etc/systemd/system/atmos.service
systemctl daemon-reload
systemctl enable atmos >/dev/null   # started by the first deployment

echo "==> Allowing deploy user to restart the service"
SUDOERS_TMP="$(mktemp)"
cat > "$SUDOERS_TMP" <<EOF
$DEPLOY_USER ALL=(root) NOPASSWD: /usr/bin/systemctl restart atmos, /usr/bin/systemctl status atmos, /usr/bin/systemctl is-active atmos
EOF
visudo -cf "$SUDOERS_TMP" >/dev/null
install -m 440 "$SUDOERS_TMP" /etc/sudoers.d/atmos-deploy
rm -f "$SUDOERS_TMP"

echo "==> Opening ports 80 and 443 in iptables"
if command -v iptables >/dev/null; then
  for port in 80 443; do
    iptables -C INPUT -p tcp --dport "$port" -m conntrack --ctstate NEW -j ACCEPT 2>/dev/null \
      || iptables -I INPUT 1 -p tcp --dport "$port" -m conntrack --ctstate NEW -j ACCEPT
  done
  if command -v netfilter-persistent >/dev/null; then
    netfilter-persistent save >/dev/null
  else
    echo "    (netfilter-persistent not installed, rules will not survive a reboot)"
  fi
fi

cat <<EOF

Setup complete.

Next steps:
  1. Edit the configuration:   sudo nano $APP_DIR/.env
  2. Add the GitHub secrets (see deploy/README.md) and push to main.
  3. Watch logs:               journalctl -u atmos -f
EOF
