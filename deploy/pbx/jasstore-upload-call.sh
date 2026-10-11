#!/usr/bin/env bash
# Opsional: unggah SATU rekaman segera setelah panggilan selesai (hook post-recording Asterisk/FreePBX).
# FreePBX: Settings > Advanced Settings > "Post Call Recording Script":
#   /usr/local/bin/jasstore-upload-call.sh ^{MIXMONITOR_FILENAME}
# Dialplan murni: MixMonitor(${REC},b,/usr/local/bin/jasstore-upload-call.sh ^{MIXMONITOR_FILENAME})
# Timer tiap menit tetap jalan sebagai jaring pengaman bila unggahan ini gagal.
set -uo pipefail
ENV_FILE=${ENV_FILE:-/etc/jasstore/sync.env}
# shellcheck disable=SC1090
source "$ENV_FILE"
export RCLONE_CONFIG=${RCLONE_CONFIG:-/etc/jasstore/rclone.conf}
FILE=${1:-}
[ -f "$FILE" ] || exit 0
case "$FILE" in "$SOURCE_DIR"/*) ;; *) exit 0 ;; esac
rel=${FILE#"$SOURCE_DIR"/}
# jalankan di latar belakang agar Asterisk tidak menunggu
( rclone copyto "$FILE" "$DEST/$rel" --retries 3 --log-file /var/log/jasstore-sync.log --log-level INFO ) &
