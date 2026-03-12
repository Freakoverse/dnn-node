# Complete DNN Deployment Guide - Fresh Server to Production

This guide takes you from a **fresh Ubuntu 24.04 server** to a **fully operational DNN node** with dashboard, DNS, HTTPS, and certificates.

> [!TIP]
> **Already have DNN deployed?** Jump to [Part 10: Quick Update for Existing Deployments](#part-10-quick-update-for-existing-deployments)

## Prerequisites

- Fresh Ubuntu 24.04 LTS server
- Root access
- Server IP: `<your-server-ip>` (or your server IP)
- Domain for dashboard: `node.icannot.xyz` (optional, for Let's Encrypt)

## Part 1: Initial Server Setup

```bash
# SSH into server
ssh root@<your-server-ip>

# Update system
apt update && apt upgrade -y

# Install required packages
apt install -y git golang-go nginx certbot python3-certbot-nginx sqlite3 build-essential unzip

# Verify Go version (should be 1.21+)
go version
```

## Part 2: Prepare and Upload DNN

### Option A: Upload from Local Machine (Recommended)

```powershell
# On your local machine (PowerShell) — from the repo root
cd <path-to-your-local-repo>

# Create minimal deployment package (Go source only, ~300 KB)
$dest = "DNN-deploy"
Remove-Item -Recurse -Force $dest -ErrorAction SilentlyContinue
New-Item -ItemType Directory -Path $dest | Out-Null

# Copy only what's needed for building
Copy-Item -Recurse DNN\node\cmd $dest\
Copy-Item -Recurse DNN\node\internal $dest\
Copy-Item DNN\node\main.go $dest\
Copy-Item DNN\node\go.mod $dest\
Copy-Item DNN\node\go.sum $dest\

# Remove daemon binaries (served via GitHub releases, not embedded)
Remove-Item -Recurse -Force "$dest\internal\node\dashboard\static\downloads" -ErrorAction SilentlyContinue

# Create zip
Remove-Item -Force "DNN-deploy.zip" -ErrorAction SilentlyContinue
Compress-Archive -Path $dest -DestinationPath DNN-deploy.zip -Force
Remove-Item -Recurse -Force $dest

# Upload to server
scp DNN-deploy.zip root@<your-server-ip>:/opt/
```

### Option B: Git Clone (if available)

```bash
cd /opt
git clone https://github.com/Freakoverse/dnn-node.git
```

### Build on Server

```bash
# SSH into server
ssh root@<your-server-ip>

# If using Option A (zip upload):
cd /opt
unzip DNN-deploy.zip
mv DNN-deploy dnn-node

# If using Option B (git clone): already in /opt/dnn-node

# Build
cd /opt/dnn-node
go build -o dnn-node .
chmod +x dnn-node

# Verify
./dnn-node --help
```

### Initialize and Configure

```bash
# Generate config.json with a fresh node keypair
./dnn-node --init

# Customize config (set your npub as admin, choose network, etc.)
# See config.example.json for all available options
nano config.json
```

Key fields to customize in `config.json`:

| Field | Default | Description |
|-------|---------|-------------|
| `network` | `testnet` | `mainnet`, `testnet`, or `dev` |
| `admin_npub` | *(empty)* | Your npub — enables the Awareness tab in the dashboard |
| `dns.enabled` | `true` | Enable DNS server (port 53 requires root) |
| `enable_awareness` | `true` | Enable the awareness filtering system |

## Part 3: Create Systemd Service

```bash
# Create service file
cat > /etc/systemd/system/dnn-node.service << 'EOF'
[Unit]
Description=DNN Node
After=network.target

[Service]
Type=simple
User=root
WorkingDirectory=/opt/dnn-node
ExecStart=/opt/dnn-node/dnn-node
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

# Enable and start service
systemctl daemon-reload
systemctl enable dnn-node
systemctl start dnn-node

# Check status
systemctl status dnn-node

# Check logs
journalctl -u dnn-node -n 50 --no-pager
```

### Enable DNS Server

> [!CAUTION]
> DNS is **disabled by default** in config.json! You must enable it.
> Also, Ubuntu's systemd-resolved uses port 53, which must be freed first.

```bash
# Step 1: Free port 53 from systemd-resolved (Ubuntu uses it by default)
sudo sed -i 's/#DNSStubListener=yes/DNSStubListener=no/' /etc/systemd/resolved.conf
sudo systemctl restart systemd-resolved

# Step 2: Edit config.json to enable DNS
cat /opt/dnn-node/config.json | sed 's/"enabled": false/"enabled": true/' | sed 's/"port": 0/"port": 53/' > /tmp/config.json && mv /tmp/config.json /opt/dnn-node/config.json

# Verify DNS is enabled in config
cat /opt/dnn-node/config.json | grep -A 3 '"dns"'

# Step 3: Restart node to apply DNS changes
systemctl restart dnn-node

# Verify DNS is listening on port 53 (should show dnn-node, not systemd-resolved)
netstat -tlnup | grep 53

# Test DNS resolution
dig <your-dnn-id> @127.0.0.1
```

## Part 4: Configure Nginx and Get SSL Certificate

### Step 1: Get Let's Encrypt Certificate First

```bash
# Remove default nginx site
rm /etc/nginx/sites-enabled/default

# Create temporary HTTP-only config for certbot
cat > /etc/nginx/sites-available/dnn-temp << 'EOF'
server {
    listen 80;
    server_name <your-domina-or-subdomain-here>;
    
    location / {
        return 200 "Temporary - getting certificate";
    }
}
EOF

# Enable temporary config
ln -s /etc/nginx/sites-available/dnn-temp /etc/nginx/sites-enabled/dnn-temp

# Test and reload nginx
nginx -t
systemctl reload nginx

# Get Let's Encrypt certificate (make sure you've added your server IP in the DNS records under your domain/sub-domain)
certbot --nginx -d <your-domina-or-subdomain-here-sub.example.com>

# Remove temporary config
rm /etc/nginx/sites-enabled/dnn-temp
rm /etc/nginx/sites-available/dnn-temp
```

### Step 2: Configure Nginx with HTTPS

```bash
# Create final nginx config with HTTPS
cat > /etc/nginx/sites-available/dnn << 'EOF'
server {
    listen 443 ssl http2;
    server_name <your-domina-or-subdomain-here-sub.example.com>;
    
    ssl_certificate /etc/letsencrypt/live/<your-domina-or-subdomain-here-sub.example.com>/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/<your-domina-or-subdomain-here-sub.example.com>/privkey.pem;
    
    location / {
        proxy_pass http://localhost:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_no_cache 1;
        proxy_cache_bypass 1;
        add_header Cache-Control "no-cache, no-store, must-revalidate" always;
    }
}

server {
    listen 80;
    server_name <your-domina-or-subdomain-here-sub.example.com>;
    return 301 https://$server_name$request_uri;
}

# Default HTTP server - redirect to HTTPS
server {
    listen 80 default_server;
    server_name _;
    return 301 https://$host$request_uri;
}
EOF

# Enable the site
ln -s /etc/nginx/sites-available/dnn /etc/nginx/sites-enabled/dnn

# Test nginx config
nginx -t

# Reload nginx
systemctl reload nginx
```

## Part 5: Generate DNN Certificate

> [!IMPORTANT]
> DNN does **not** require any specific fields in the certificate (no SAN, no matching CN).
> DNN trust comes from the **62600 Nostr event** — the cert PEM you publish there must match the cert your server uses during TLS handshake. That's it.
>
> You can use any self-signed certificate. The browser verifies: `server cert PEM === declared cert PEM in 62600 event`.

### Generate a Self-Signed Certificate

```bash
# Create certs directory
mkdir -p /opt/certs
cd /opt/certs

# Generate private key + self-signed certificate (valid ~1000 years)
openssl req -x509 -newkey rsa:2048 -nodes \
    -keyout dnn-server.key \
    -out dnn-server.crt \
    -days 365000 \
    -subj "/CN=DNN Server"

echo "✅ Certificate generated"
```

This creates:
- `dnn-server.key` — Private key (keep secure!)
- `dnn-server.crt` — Self-signed certificate

### Copy Certificate for Connection Event

```bash
# Display cert PEM - copy this for your connection event
cat /opt/certs/dnn-server.crt
```

> [!TIP]
> Copy the PEM output and paste it into the **SSL/TLS Certificate** field in the dashboard's Connection Event form.
> The signed Nostr event proves you (the name owner) declared this certificate for your DNN name.

## Part 6: Configure Nginx for DNN Name

```bash
# Create nginx config for your DNN name
cat > /etc/nginx/sites-available/dnn-name << 'EOF'
server {
    listen 443 ssl http2 default_server;
    server_name _;
    
    ssl_certificate /opt/certs/dnn-server.crt;
    ssl_certificate_key /opt/certs/dnn-server.key;
    
    # Proxy to your application (e.g., dashboard)
    location / {
        proxy_pass http://localhost:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_no_cache 1;
        proxy_cache_bypass 1;
        add_header Cache-Control "no-cache, no-store, must-revalidate" always;
    }
}
EOF

# Enable the site
ln -s /etc/nginx/sites-available/dnn-name /etc/nginx/sites-enabled/dnn-name

# Test and reload nginx
nginx -t
systemctl reload nginx
```

> [!NOTE]
> Since this nginx block uses `default_server`, it will catch all DNN-resolved HTTPS traffic hitting this server's IP — no per-name configuration needed. Just publish the same cert in each 62600 event.

## Part 7: Quick Update for Existing Deployments (optional)

> [!IMPORTANT]
> This section is for updating an **existing DNN deployment** with new code.

### Option A: Git Pull (Recommended)

```bash
# SSH into server
ssh root@<your-server-ip>

# Stop the service
systemctl stop dnn-node

# Pull latest code
cd /opt/dnn-node
git pull origin main

# Rebuild
go build -o dnn-node .
chmod +x dnn-node

# Restart service
systemctl start dnn-node

# Verify
systemctl status dnn-node
journalctl -u dnn-node -n 20 --no-pager
```

### Option B: Upload ZIP

```bash
# SSH into server
ssh root@<your-server-ip>

# Stop the service
systemctl stop dnn-node

# Backup current installation
cd /opt
mv dnn-node dnn-node-backup-$(date +%Y%m%d)

# Extract new version
unzip DNN-update.zip
mv DNN-deploy dnn-node

# Copy database and config from backup
cp -r dnn-node-backup-*/data dnn-node/
cp dnn-node-backup-*/config.json dnn-node/

# Build new version
cd dnn-node
go build -o dnn-node .
chmod +x dnn-node

# Start service
systemctl start dnn-node

# Verify
systemctl status dnn-node
journalctl -u dnn-node -n 20 --no-pager
```

---

## Files and Directories

```
/opt/dnn-node/
├── dnn-node              # Main binary
├── internal/             # Source code
├── data/
│   └── dnn.db            # SQLite database
└── config.json           # Configuration

/opt/certs/
├── dnn-server.crt        # Self-signed certificate (published in 62600 event)
└── dnn-server.key        # Private key (keep secure!)
```

### Awareness Configuration (config.json)

To enable the Awareness Database, add these fields to your `config.json`:

```json
{
  "enable_awareness": true,
  "admin_npub": "npub1your_admin_npub_here"
}
```

| Field | Description |
|-------|-------------|
| `enable_awareness` | Enable/disable the awareness system |
| `admin_npub` | Npub of the admin who manages awareness marks (can differ from node npub) |

## Summary

You now have:
- ✅ DNN node running and syncing with Bitcoin/Nostr
- ✅ Dashboard accessible via HTTPS
- ✅ DNS server resolving DNN names
- ✅ Self-signed certificate for DNN name access
- ✅ API returns cert and connection data for browser/daemon verification

**Total setup time**: ~30 minutes on a fresh server!

---

## Updating Daemon Binaries

The DNN daemon installers are embedded in the node binary. To update them:

```bash
# On your local machine - build new daemon binaries
cd DNN/daemon
go build -o ../internal/node/dashboard/static/downloads/dnn-daemon.exe ./cmd/dnn-daemon
GOOS=linux GOARCH=amd64 go build -o ../internal/node/dashboard/static/downloads/dnn-daemon-linux ./cmd/dnn-daemon
GOOS=darwin GOARCH=amd64 go build -o ../internal/node/dashboard/static/downloads/dnn-daemon-macos ./cmd/dnn-daemon

# Rebuild the node (this embeds the new daemon binaries)
cd ..
go build -o dnn-node .

# Upload and deploy as usual
```

Users can download the daemon from the dashboard's "DNN Daemon" section.

