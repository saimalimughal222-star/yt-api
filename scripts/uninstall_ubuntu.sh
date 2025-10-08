#!/usr/bin/env bash
set -euo pipefail

APP_NAME="ytmp3-api"
APP_USER="ytmp3"
APP_GROUP="ytmp3"
INSTALL_DIR="/opt/${APP_NAME}"
BIN_PATH="/usr/local/bin/${APP_NAME}"
SERVICE_FILE="/etc/systemd/system/${APP_NAME}.service"
NGINX_SITE="/etc/nginx/sites-available/${APP_NAME}.conf"
NGINX_LINK="/etc/nginx/sites-enabled/${APP_NAME}.conf"

if [[ $EUID -ne 0 ]]; then
  echo "Please run as root" >&2
  exit 1
fi

systemctl stop ${APP_NAME}.service || true
systemctl disable ${APP_NAME}.service || true
rm -f "${SERVICE_FILE}"
systemctl daemon-reload

rm -f "${NGINX_LINK}" "${NGINX_SITE}"
systemctl reload nginx || true

rm -f "${BIN_PATH}"
rm -rf "${INSTALL_DIR}"

# Optionally remove user/group
if id -u ${APP_USER} >/dev/null 2>&1; then
  deluser --system ${APP_USER} || true
fi
if getent group ${APP_GROUP} >/dev/null 2>&1; then
  delgroup ${APP_GROUP} || true
fi

echo "Uninstall complete."
