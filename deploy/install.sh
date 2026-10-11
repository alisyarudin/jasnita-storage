#!/usr/bin/env bash
# Pasang jasstore di server Linux (systemd). Jalankan sebagai root setelah
# menyalin bin/jasstore-linux-amd64 dan deploy/ ke server.
set -euo pipefail
BIN=${1:-./jasstore-linux-amd64}
id -u jasstore >/dev/null 2>&1 || useradd --system --home /var/lib/jasstore --shell /usr/sbin/nologin jasstore
mkdir -p /var/lib/jasstore /etc/jasstore
install -m 0755 "$BIN" /usr/local/bin/jasstore
if [ ! -f /etc/jasstore/jasstore.env ]; then
  SECRET=$(head -c 32 /dev/urandom | base64 | tr -d '/+=' | head -c 40)
  cat > /etc/jasstore/jasstore.env <<ENV
JASSTORE_DATA_DIR=/var/lib/jasstore
JASSTORE_S3_ADDR=127.0.0.1:9000
JASSTORE_CONSOLE_ADDR=127.0.0.1:9001
JASSTORE_REGION=ap-southeast-1
JASSTORE_PUBLIC_URL=https://s3.jascall.com
JASSTORE_ROOT_ACCESS_KEY=jasstore
JASSTORE_ROOT_SECRET_KEY=$SECRET
ENV
  chmod 0600 /etc/jasstore/jasstore.env
  echo "Root secret dibuat di /etc/jasstore/jasstore.env"
fi
chown -R jasstore:jasstore /var/lib/jasstore
install -m 0644 "$(dirname "$0")/jasstore.service" /etc/systemd/system/jasstore.service
systemctl daemon-reload
systemctl enable --now jasstore
systemctl --no-pager status jasstore | head -5
