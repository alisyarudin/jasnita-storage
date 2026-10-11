#!/usr/bin/env bash
# Pasang sinkronisasi rekaman di PBX (jalankan sebagai root di PBX).
# Pakai: ./install-pbx.sh  (berkas lain di folder yang sama)
set -euo pipefail
D=$(cd "$(dirname "$0")" && pwd)
command -v rclone >/dev/null || curl -fsSL https://rclone.org/install.sh | bash
mkdir -p /etc/jasstore
[ -f /etc/jasstore/rclone.conf ] || { install -m 0600 "$D/rclone.conf.example" /etc/jasstore/rclone.conf; echo "ISI /etc/jasstore/rclone.conf (access key, secret, endpoint)"; }
[ -f /etc/jasstore/sync.env ]    || { install -m 0644 "$D/jasstore-sync.env.example" /etc/jasstore/sync.env; echo "PERIKSA /etc/jasstore/sync.env (SOURCE_DIR, DEST, MODE)"; }
install -m 0755 "$D/jasstore-sync.sh" /usr/local/bin/jasstore-sync.sh
install -m 0755 "$D/jasstore-upload-call.sh" /usr/local/bin/jasstore-upload-call.sh
install -m 0644 "$D/jasstore-sync.service" /etc/systemd/system/jasstore-sync.service
install -m 0644 "$D/jasstore-sync.timer" /etc/systemd/system/jasstore-sync.timer
touch /var/log/jasstore-sync.log
systemctl daemon-reload
systemctl enable --now jasstore-sync.timer
echo "Uji koneksi:"; RCLONE_CONFIG=/etc/jasstore/rclone.conf rclone lsd jasstore: || echo "  -> perbaiki /etc/jasstore/rclone.conf lalu: systemctl start jasstore-sync.service"
systemctl list-timers jasstore-sync.timer --no-pager
