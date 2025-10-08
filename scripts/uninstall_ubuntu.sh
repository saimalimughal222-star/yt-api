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
ENV_FILE="/etc/${APP_NAME}.env"

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
rm -f "${ENV_FILE}"

# Optional: clear data (downloads and Redis keys)
read -r -p "Clear downloads folder ${INSTALL_DIR}/downloads? [Y/n] " _ans || _ans="Y"
if [[ "${_ans}" =~ ^(Y|y|)$ ]]; then
  rm -rf "${INSTALL_DIR}/downloads" || true
fi

if command -v redis-cli >/dev/null 2>&1; then
  read -r -p "Clear Redis keys job:* and url:* on localhost:6379? [Y/n] " _r || _r="Y"
  if [[ "${_r}" =~ ^(Y|y|)$ ]]; then
    redis-cli --scan --pattern 'job:*' | xargs -r redis-cli del || true
    redis-cli --scan --pattern 'url:*' | xargs -r redis-cli del || true
  fi
fi

# Optionally remove user/group
if id -u ${APP_USER} >/dev/null 2>&1; then
  deluser --system ${APP_USER} || true
fi
if getent group ${APP_GROUP} >/dev/null 2>&1; then
  delgroup ${APP_GROUP} || true
fi

echo "Uninstall complete."
