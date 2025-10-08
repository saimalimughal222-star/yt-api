# Ubuntu Deployment - YouTube to MP3 API

This guide shows two setups:
- Quick install on a server IP (no domain)
- Domain + HTTPS (automatic Let's Encrypt)

## 1) Requirements
- Ubuntu 20.04/22.04 (root or sudo)
- Open ports: 80 (HTTP), 443 (HTTPS) if using domain
- A domain pointing to your server's public IP (A record) for HTTPS

## 2) Quick install (no domain)
This installs dependencies, builds the API, configures systemd and Nginx on port 80.

```bash
sudo bash scripts/install_ubuntu.sh
```

Check service:
```bash
sudo systemctl status ytmp3-api --no-pager
```

Open in browser:
- API: http://YOUR_SERVER_IP/
- Health: http://YOUR_SERVER_IP/health
- Extract: POST http://YOUR_SERVER_IP/extract

Uninstall later:
```bash
sudo bash scripts/uninstall_ubuntu.sh
```

## 3) Using your domain (HTTP only)
Edit Nginx site to set your domain and reload.

```bash
sudo sed -i 's/server_name _;/server_name api.example.com;/' /etc/nginx/sites-available/ytmp3-api.conf
sudo nginx -t && sudo systemctl reload nginx
```

Now visit: http://api.example.com/

## 4) Enable HTTPS (automatic)
Use Certbot (Let's Encrypt) to get a TLS certificate and auto-renew.

```bash
sudo apt-get update
sudo apt-get install -y certbot python3-certbot-nginx

# Replace with your domain
DOMAIN=api.example.com

# Issue certificate and update Nginx automatically
sudo certbot --nginx -d "$DOMAIN" --non-interactive --agree-tos -m you@example.com --redirect

# Test auto-renew (dry run)
sudo certbot renew --dry-run
```

This adds HTTPS and configures HTTP->HTTPS redirect in Nginx. Your API will be served at:
- https://api.example.com/

## 5) Scaling and rate limits
- Adjust rate limit in Nginx: edit `/etc/nginx/sites-available/ytmp3-api.conf`
- Tune workers in systemd env: `WORKER_POOL_SIZE`, `REQUESTS_PER_SECOND`

Apply changes:
```bash
sudo systemctl restart ytmp3-api
sudo systemctl reload nginx
```

## 6) Logs and files
- API logs: `journalctl -u ytmp3-api -f`
- Downloads: `/var/lib/ytmp3-api/downloads`

## 7) Firewall (optional)
If UFW is enabled:
```bash
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
```

## 8) Troubleshooting
- Check health: `curl http://localhost/health`
- Service status: `systemctl status ytmp3-api`
- Nginx test: `nginx -t`
- Redis status: `systemctl status redis-server`

