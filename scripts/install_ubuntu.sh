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
NGINX_LIMITS="/etc/nginx/conf.d/${APP_NAME}-limits.conf"
ENV_FILE="/etc/${APP_NAME}.env"

if [[ $EUID -ne 0 ]]; then
  echo "Please run as root" >&2
  exit 1
fi

apt-get update -y
apt-get install -y --no-install-recommends \
  curl ca-certificates gnupg lsb-release \
  build-essential git \
  ffmpeg python3 python3-pip \
  redis-server nginx

pip3 install --break-system-packages -U yt-dlp

# Ensure Go toolchain (use existing if available, else install 1.24.2)
GO_BIN=""
if [[ -x "/usr/local/go/bin/go" ]]; then
  GO_BIN="/usr/local/go/bin/go"
elif command -v go >/dev/null 2>&1; then
  GO_BIN="$(command -v go)"
else
  GO_VER="1.24.2"
  curl -fsSL "https://go.dev/dl/go${GO_VER}.linux-amd64.tar.gz" -o /tmp/go.tgz
  rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz
  echo 'export PATH=/usr/local/go/bin:$PATH' > /etc/profile.d/go.sh
  export PATH=/usr/local/go/bin:$PATH
  GO_BIN="/usr/local/go/bin/go"
fi

# Create user/group
if ! id -u ${APP_USER} >/dev/null 2>&1; then
  adduser --system --group --no-create-home ${APP_USER}
fi

# Build application
WORKDIR=$(pwd)
mkdir -p "${INSTALL_DIR}"
cp -r "${WORKDIR}"/* "${INSTALL_DIR}/"
cd "${INSTALL_DIR}"
"${GO_BIN}" version || true
"${GO_BIN}" mod tidy
"${GO_BIN}" clean -cache
"${GO_BIN}" build -o "${BIN_PATH}" .
chown -R ${APP_USER}:${APP_GROUP} "${INSTALL_DIR}"
chown ${APP_USER}:${APP_GROUP} "${BIN_PATH}"
chmod 0755 "${BIN_PATH}"

# Create downloads dir inside install root
mkdir -p ${INSTALL_DIR}/downloads
chown -R ${APP_USER}:${APP_GROUP} ${INSTALL_DIR}

# Systemd detection (we will create service later after prompts)
HAS_SYSTEMD=false
if command -v systemctl >/dev/null 2>&1 && pidof systemd >/dev/null 2>&1; then
  HAS_SYSTEMD=true
fi

# Interactive options and Nginx configuration
ask_yes_no() {
  local prompt="$1"; local default=${2:-Y}; local reply; local suffix="[Y/n]"; [[ "$default" == "N" ]] && suffix="[y/N]"
  while true; do
    read -r -p "$prompt $suffix " reply || reply=""
    [[ -z "$reply" ]] && reply="$default"
    case "$reply" in
      Y|y|yes|YES) return 0;;
      N|n|no|NO) return 1;;
      *) echo "Please answer yes or no.";;
    esac
  done
}

read_with_default() {
  local prompt="$1"; local def="$2"; local var
  read -r -p "$prompt [$def]: " var || var=""
  echo "${var:-$def}"
}

WORKER_POOL_SIZE=$(read_with_default "Worker pool size" "20")
JOB_QUEUE_CAPACITY=$(read_with_default "Job queue capacity" "1000")
REQUESTS_PER_SECOND=$(read_with_default "App rate limit (req/s)" "100")
BURST_SIZE=$(read_with_default "App rate burst" "200")

# Abuse protection & auth
REQUIRE_API_KEY_ANS=$(read_with_default "Require API key? (true/false)" "false")
if [[ "${REQUIRE_API_KEY_ANS}" == "true" ]]; then
  API_KEYS=$(read_with_default "Enter comma-separated API keys" "")
else
  API_KEYS=""
fi
PER_IP_RPS=$(read_with_default "Per-IP request rate (req/s)" "10")
PER_IP_BURST=$(read_with_default "Per-IP burst" "20")

# Networking / CORS
ALLOWED_ORIGINS=$(read_with_default "Allowed origins for CORS (comma or *)" "*")

# Redis
if ask_yes_no "Use LOCAL Redis at localhost:6379?" Y; then
  REDIS_ADDR="localhost:6379"
else
  REDIS_ADDR=$(read_with_default "Enter Redis address host:port" "localhost:6379")
fi

# Durations (use Go duration format: 24h, 30s, etc.)
JOB_EXPIRATION=$(read_with_default "Job expiration (metadata TTL)" "24h")
HEALTH_CHECK_INTERVAL=$(read_with_default "Health check interval" "30s")
FAST_PATH_WAIT=$(read_with_default "Fast-path wait (for quick jobs)" "8s")

# Retry backoff
BACKOFF_BASE_SECONDS=$(read_with_default "Backoff base seconds" "5")
BACKOFF_MAX_SECONDS=$(read_with_default "Backoff max seconds" "60")

# Max video duration (minutes); blank to disable
MAX_DURATION_MIN=$(read_with_default "Max video duration minutes (blank = no limit)" "90")

# Optional: clear previous data before starting (Redis keys and downloads)
if ask_yes_no "Clear existing data now (Redis job/url keys and downloads folder)?" Y; then
  echo "Clearing downloads in ${INSTALL_DIR}/downloads ..."
  rm -rf ${INSTALL_DIR}/downloads/* || true
  if command -v redis-cli >/dev/null 2>&1; then
    echo "Clearing Redis keys job:* and url:* on localhost:6379 ..."
    redis-cli --scan --pattern 'job:*' | xargs -r redis-cli del || true
    redis-cli --scan --pattern 'url:*' | xargs -r redis-cli del || true
  else
    echo "redis-cli not found; skipping Redis key cleanup"
  fi
fi

if ask_yes_no "Configure Nginx reverse proxy (domain or IP)?" Y; then
  DOMAIN=$(read_with_default "Enter domain (blank = use server IP)" "")
  # Global limits (http context)
  NGINX_API_RPS=$(read_with_default "Nginx limit for /extract and /status (req/s)" "10")
  NGINX_DOWNLOAD_RPS=$(read_with_default "Nginx limit for /download (req/s)" "5")
  cat >"${NGINX_LIMITS}" <<EOF
limit_req_zone $binary_remote_addr zone=api_limit:10m rate=${NGINX_API_RPS}r/s;
limit_req_zone $binary_remote_addr zone=download_limit:10m rate=${NGINX_DOWNLOAD_RPS}r/s;
limit_conn_zone $binary_remote_addr zone=conn_limit_per_ip:10m;
EOF

  # Server block
  cat >"${NGINX_SITE}" <<EOF
server {
    listen 80;
    server_name ${DOMAIN:-_};

    add_header X-Frame-Options DENY;
    add_header X-Content-Type-Options nosniff;

    location /health { proxy_pass http://127.0.0.1:8080; }

    location /extract {
        limit_req zone=api_limit burst=20 nodelay;
        limit_conn conn_limit_per_ip 20;
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }

    location /status/ { proxy_pass http://127.0.0.1:8080; }
    location /download/ {
        limit_req zone=download_limit burst=10 nodelay;
        proxy_pass http://127.0.0.1:8080;
    }
    location /metrics { allow 127.0.0.1; deny all; proxy_pass http://127.0.0.1:8080; }
    location / { proxy_pass http://127.0.0.1:8080; }
}
EOF

  ln -sf "${NGINX_SITE}" "${NGINX_LINK}"
  if [[ "${HAS_SYSTEMD}" == "true" ]]; then
    nginx -t && systemctl reload nginx
  else
    nginx -t && nginx -s reload || true
  fi

  if [[ -n "${DOMAIN}" ]] && ask_yes_no "Enable HTTPS for ${DOMAIN} with Let's Encrypt?" N; then
    EMAIL=$(read_with_default "Admin email for Let's Encrypt" "you@example.com")
    apt-get install -y certbot python3-certbot-nginx
    certbot --nginx -d "${DOMAIN}" --non-interactive --agree-tos -m "${EMAIL}" --redirect || true
  fi
fi

# Redis enable and start
if [[ "${HAS_SYSTEMD}" == "true" ]]; then
  systemctl enable redis-server.service
  systemctl restart redis-server.service
else
  # Attempt to start Redis without systemd
  service redis-server start || redis-server --daemonize yes || true
fi

# Write environment file for systemd and create service
REQUESTS_PER_SECOND=${REQUESTS_PER_SECOND}
BURST_SIZE=${BURST_SIZE}
cat >"${ENV_FILE}" <<EOF
REDIS_ADDR=${REDIS_ADDR}
REQUESTS_PER_SECOND=${REQUESTS_PER_SECOND}
BURST_SIZE=${BURST_SIZE}
WORKER_POOL_SIZE=${WORKER_POOL_SIZE}
JOB_QUEUE_CAPACITY=${JOB_QUEUE_CAPACITY}
MAX_JOB_RETRIES=3
JOB_EXPIRATION=${JOB_EXPIRATION}
HEALTH_CHECK_INTERVAL=${HEALTH_CHECK_INTERVAL}
FAST_PATH_WAIT=${FAST_PATH_WAIT}
ALLOWED_ORIGINS=${ALLOWED_ORIGINS}
REQUIRE_API_KEY=${REQUIRE_API_KEY_ANS}
API_KEYS=${API_KEYS}
PER_IP_RPS=${PER_IP_RPS}
PER_IP_BURST=${PER_IP_BURST}
BACKOFF_BASE_SECONDS=${BACKOFF_BASE_SECONDS}
BACKOFF_MAX_SECONDS=${BACKOFF_MAX_SECONDS}
MAX_DURATION_MIN=${MAX_DURATION_MIN}
EOF
chmod 0644 "${ENV_FILE}"

if [[ "${HAS_SYSTEMD}" == "true" ]]; then
cat >"${SERVICE_FILE}" <<EOF
[Unit]
Description=YouTube to MP3 API
After=network.target redis-server.service

[Service]
User=ytmp3
Group=ytmp3
EnvironmentFile=${ENV_FILE}
Environment=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin
ExecStart=/usr/local/bin/ytmp3-api
WorkingDirectory=${INSTALL_DIR}
Restart=on-failure
RestartSec=3
LimitNOFILE=65536
NoNewPrivileges=true
ProtectSystem=full
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable ${APP_NAME}.service
systemctl restart ${APP_NAME}.service
else
  cat >"/usr/local/bin/${APP_NAME}-run" <<EOF
#!/usr/bin/env bash
set -euo pipefail
source ${ENV_FILE} || true
cd ${INSTALL_DIR}
nohup /usr/local/bin/ytmp3-api >${INSTALL_DIR}/ytmp3-api.log 2>&1 &
echo $! > ${INSTALL_DIR}/ytmp3-api.pid
echo "Started ytmp3-api (PID $(cat ${INSTALL_DIR}/ytmp3-api.pid))"
EOF
  chmod +x "/usr/local/bin/${APP_NAME}-run"
  "/usr/local/bin/${APP_NAME}-run" || true
fi

# Done
systemctl status ${APP_NAME}.service --no-pager || true

echo "Installation complete. API available on http://<server>:80 and http://localhost:8080"
