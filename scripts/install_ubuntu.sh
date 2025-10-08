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
"${GO_BIN}" build -o "${BIN_PATH}" .
chown -R ${APP_USER}:${APP_GROUP} "${INSTALL_DIR}"
chown ${APP_USER}:${APP_GROUP} "${BIN_PATH}"
chmod 0755 "${BIN_PATH}"

# Create downloads dir
mkdir -p /var/lib/${APP_NAME}/downloads
chown -R ${APP_USER}:${APP_GROUP} /var/lib/${APP_NAME}

# Systemd service
HAS_SYSTEMD=false
if command -v systemctl >/dev/null 2>&1 && pidof systemd >/dev/null 2>&1; then
  HAS_SYSTEMD=true
fi

if [[ "${HAS_SYSTEMD}" == "true" ]]; then
cat >/etc/systemd/system/${APP_NAME}.service <<'EOF'
[Unit]
Description=YouTube to MP3 API
After=network.target redis-server.service

[Service]
User=ytmp3
Group=ytmp3
ExecStart=/usr/local/bin/ytmp3-api
WorkingDirectory=/var/lib/ytmp3-api
Environment=REDIS_ADDR=localhost:6379
Environment=REQUESTS_PER_SECOND=${REQUESTS_PER_SECOND}
Environment=BURST_SIZE=${BURST_SIZE}
Environment=WORKER_POOL_SIZE=${WORKER_POOL_SIZE}
Environment=JOB_QUEUE_CAPACITY=${JOB_QUEUE_CAPACITY}
Restart=on-failure
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable ${APP_NAME}.service
systemctl restart ${APP_NAME}.service
else
  # Fallback for environments without systemd (e.g., WSL without systemd)
  cat >/usr/local/bin/${APP_NAME}-run <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
export REDIS_ADDR=localhost:6379
export REQUESTS_PER_SECOND=${REQUESTS_PER_SECOND}
export BURST_SIZE=${BURST_SIZE}
export WORKER_POOL_SIZE=${WORKER_POOL_SIZE}
export JOB_QUEUE_CAPACITY=${JOB_QUEUE_CAPACITY}
cd /var/lib/ytmp3-api
nohup /usr/local/bin/ytmp3-api >/var/lib/ytmp3-api/ytmp3-api.log 2>&1 &
echo $! > /var/lib/ytmp3-api/ytmp3-api.pid
echo "Started ytmp3-api (PID $(cat /var/lib/ytmp3-api/ytmp3-api.pid))"
EOF
  chmod +x /usr/local/bin/${APP_NAME}-run
  /usr/local/bin/${APP_NAME}-run || true
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

if ask_yes_no "Configure Nginx reverse proxy (domain or IP)?" Y; then
  DOMAIN=$(read_with_default "Enter domain (blank = use server IP)" "")
  # Global limits (http context)
  cat >"${NGINX_LIMITS}" <<'EOF'
limit_req_zone $binary_remote_addr zone=api_limit:10m rate=10r/s;
limit_req_zone $binary_remote_addr zone=download_limit:10m rate=5r/s;
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

# Done
systemctl status ${APP_NAME}.service --no-pager || true

echo "Installation complete. API available on http://<server>:80"
