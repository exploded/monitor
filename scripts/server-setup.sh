#!/bin/bash
# server-setup.sh
#
# One-time setup script to prepare your Linode Debian server for automated
# deployments of the monitor portal from GitHub Actions.
#
# Run as root or with sudo:
#   sudo bash scripts/server-setup.sh
#
# After running, follow the printed instructions to add the SSH public key
# to your GitHub repository secrets.

set -e

DEPLOY_USER="deploy"
APP_DIR="/var/www/monitor"
SERVICE_NAME="monitor"

echo "=== Monitor Portal - Server Deployment Setup ==="
echo ""

# ---------------------------------------------------------------
# 1. Create deploy user (if not already present)
# ---------------------------------------------------------------
if id "$DEPLOY_USER" &>/dev/null; then
    echo "[ok] User '$DEPLOY_USER' already exists"
else
    useradd -m -s /bin/bash "$DEPLOY_USER"
    echo "[ok] Created user '$DEPLOY_USER'"
fi

# ---------------------------------------------------------------
# 2. Generate SSH key pair for GitHub Actions
# ---------------------------------------------------------------
KEY_DIR="/home/$DEPLOY_USER/.ssh"
KEY_FILE="$KEY_DIR/github_actions"

mkdir -p "$KEY_DIR"
chmod 700 "$KEY_DIR"

if [ ! -f "$KEY_FILE" ]; then
    ssh-keygen -t ed25519 -f "$KEY_FILE" -N "" -C "github-actions-monitor-deploy"
    echo "[ok] Generated SSH key pair at $KEY_FILE"
else
    echo "[ok] SSH key already exists at $KEY_FILE"
fi

# Authorise the key for the deploy user
if ! grep -qF "$(cat "$KEY_FILE.pub")" "$KEY_DIR/authorized_keys" 2>/dev/null; then
    cat "$KEY_FILE.pub" >> "$KEY_DIR/authorized_keys"
    echo "[ok] Public key added to authorized_keys"
fi

chmod 600 "$KEY_DIR/authorized_keys"
chown -R "$DEPLOY_USER:$DEPLOY_USER" "$KEY_DIR"

# ---------------------------------------------------------------
# 3. Create application directory
# ---------------------------------------------------------------
mkdir -p "$APP_DIR"/{db,web/static/css,web/static/js,web/templates}
chown -R www-data:www-data "$APP_DIR"

echo "[ok] Created $APP_DIR"

# ---------------------------------------------------------------
# 4. Create .env template (if not present)
# ---------------------------------------------------------------
if [ ! -f "$APP_DIR/.env" ]; then
    cat > "$APP_DIR/.env" << 'ENVFILE'
PORT=8484
DB_PATH=/var/www/monitor/monitor.db
LOG_PATH=/var/log/caddy/access.log
CADDY_ADMIN_URL=http://localhost:2019
AUTH_USER=admin
AUTH_PASS=CHANGE_ME_TO_A_STRONG_PASSWORD
RETENTION_DAYS=90
ENVFILE
    chown www-data:www-data "$APP_DIR/.env"
    chmod 600 "$APP_DIR/.env"
    echo "[ok] Created .env template at $APP_DIR/.env — EDIT THE PASSWORD!"
else
    echo "[ok] .env already exists at $APP_DIR/.env"
fi

# ---------------------------------------------------------------
# 5. Install systemd service
# ---------------------------------------------------------------
cat > "/etc/systemd/system/$SERVICE_NAME.service" << 'SERVICEFILE'
[Unit]
Description=Server Monitor Portal
After=network.target caddy.service

[Service]
Type=simple
User=www-data
Group=www-data
WorkingDirectory=/var/www/monitor
ExecStart=/var/www/monitor/monitor
EnvironmentFile=/var/www/monitor/.env
Restart=on-failure
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
SERVICEFILE

systemctl daemon-reload
systemctl enable "$SERVICE_NAME"
echo "[ok] Installed and enabled $SERVICE_NAME.service"

# ---------------------------------------------------------------
# 6. Create the server-side deploy script
# ---------------------------------------------------------------
cat > /usr/local/bin/deploy-monitor << 'DEPLOY_SCRIPT'
#!/bin/bash
set -e

DEPLOY_SRC="${1:-/tmp/monitor-deploy}"
DEPLOY_DIR=/var/www/monitor

BUNDLE_SCRIPT="$DEPLOY_SRC/scripts/deploy-monitor"
if [ -f "$BUNDLE_SCRIPT" ] && ! diff -q /usr/local/bin/deploy-monitor "$BUNDLE_SCRIPT" > /dev/null 2>&1; then
    echo "[deploy] Updating deploy script from bundle..."
    install -m 755 "$BUNDLE_SCRIPT" /usr/local/bin/deploy-monitor
    exec /usr/local/bin/deploy-monitor "$@"
fi

SERVICE_USER=$(systemctl show monitor --property=User --value)
SERVICE_GROUP=$(systemctl show monitor --property=Group --value)

if [ -z "$SERVICE_USER" ]; then
    echo "[deploy] ERROR: Could not read User from monitor.service"
    exit 1
fi

echo "[deploy] Stopping service..."
systemctl stop monitor || true

echo "[deploy] Installing binary..."
rm -f "$DEPLOY_DIR/monitor"
cp "$DEPLOY_SRC/monitor" "$DEPLOY_DIR/monitor"
chmod +x "$DEPLOY_DIR/monitor"

echo "[deploy] Updating web assets..."
cp -r "$DEPLOY_SRC/web/" "$DEPLOY_DIR/"

echo "[deploy] Updating database schema..."
mkdir -p "$DEPLOY_DIR/db"
cp "$DEPLOY_SRC/db/schema.sql" "$DEPLOY_DIR/db/"

chown -R "$SERVICE_USER:$SERVICE_GROUP" "$DEPLOY_DIR"

echo "[deploy] Starting service..."
systemctl start monitor

echo "[deploy] Verifying service is active..."
sleep 2
if ! systemctl is-active --quiet monitor; then
    echo "[deploy] ERROR: Service failed to start. Status:"
    systemctl status monitor --no-pager --lines=30
    exit 1
fi

echo "[deploy] Cleaning up..."
rm -rf "$DEPLOY_SRC"

echo "[deploy] Done — monitor is running."
DEPLOY_SCRIPT

chmod +x /usr/local/bin/deploy-monitor
echo "[ok] Created /usr/local/bin/deploy-monitor"

# ---------------------------------------------------------------
# 7. Configure sudoers — only allow the deploy script + service stop
# ---------------------------------------------------------------
SUDOERS_FILE="/etc/sudoers.d/monitor-deploy"

cat > "$SUDOERS_FILE" << 'EOF'
# Allow the deploy user to run the monitor deployment script as root
deploy ALL=(ALL) NOPASSWD: /usr/local/bin/deploy-monitor
# Allow stopping the monitor service directly (used by GitHub Actions)
deploy ALL=(ALL) NOPASSWD: /usr/bin/systemctl stop monitor
EOF

chmod 440 "$SUDOERS_FILE"
visudo -c -f "$SUDOERS_FILE"
echo "[ok] sudoers entry created at $SUDOERS_FILE"

# ---------------------------------------------------------------
# 8. Ensure www-data can read Caddy logs
# ---------------------------------------------------------------
if getent group caddy > /dev/null 2>&1; then
    usermod -aG caddy www-data
    echo "[ok] Added www-data to caddy group (for log access)"
else
    echo "[info] caddy group not found — ensure www-data can read the Caddy log file"
fi

# ---------------------------------------------------------------
# 9. Print next steps
# ---------------------------------------------------------------
echo ""
echo "=== Setup complete. Add these secrets to your GitHub repository: ==="
echo ""
echo "Go to: GitHub repo -> Settings -> Secrets and variables -> Actions"
echo ""
echo "Secret name     : DEPLOY_HOST"
echo "Secret value    : $(hostname -I | awk '{print $1}')  (your server's public IP)"
echo ""
echo "Secret name     : DEPLOY_USER"
echo "Secret value    : $DEPLOY_USER"
echo ""
echo "Secret name     : DEPLOY_SSH_KEY"
echo "Secret value    : (paste the private key below)"
echo ""
echo "---BEGIN PRIVATE KEY (copy everything including the dashes)---"
cat "$KEY_FILE"
echo "---END PRIVATE KEY---"
echo ""
echo "Optional secret : DEPLOY_PORT  (only if SSH is not on port 22)"
echo ""
echo "IMPORTANT: Edit $APP_DIR/.env and set a strong AUTH_PASS before starting."
echo ""
echo "After adding secrets, push to master to trigger your first deployment."
