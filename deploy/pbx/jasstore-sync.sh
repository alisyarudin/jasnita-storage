#!/usr/bin/env bash
# Sinkronkan rekaman PBX ke JasStore memakai rclone. Dijalankan oleh systemd timer tiap menit.
set -euo pipefail
ENV_FILE=${ENV_FILE:-/etc/jasstore/sync.env}
# shellcheck disable=SC1090
source "$ENV_FILE"
export RCLONE_CONFIG=${RCLONE_CONFIG:-/etc/jasstore/rclone.conf}
LOG=${LOG:-/var/log/jasstore-sync.log}
LOCK=/run/lock/jasstore-sync.lock

exec 9>"$LOCK"
if ! flock -n 9; then
  echo "$(date '+%F %T') masih ada sinkronisasi yang berjalan, lewati" >> "$LOG"
  exit 0
fi

IFS=',' read -ra PATTERNS <<< "${INCLUDE:-*.wav}"
INCLUDE_ARGS=()
for p in "${PATTERNS[@]}"; do INCLUDE_ARGS+=(--include "$p"); done

COMMON=(--min-age "${MIN_AGE:-2m}" --transfers "${TRANSFERS:-4}" --checkers 8 \
  --s3-upload-cutoff 16M --s3-chunk-size 16M --retries 3 --low-level-retries 5 \
  --log-file "$LOG" --log-level INFO --stats-one-line --stats 0 "${INCLUDE_ARGS[@]}")

case "${MODE:-copy}" in
  move)
    rclone move "$SOURCE_DIR" "$DEST" "${COMMON[@]}" --delete-empty-src-dirs
    ;;
  copy)
    rclone copy "$SOURCE_DIR" "$DEST" "${COMMON[@]}"
    if [ "${KEEP_DAYS:-0}" -gt 0 ]; then
      # Hapus lokal yang sudah > KEEP_DAYS hari DAN terbukti sama di storage (rclone check per berkas).
      find "$SOURCE_DIR" -type f -mtime +"$KEEP_DAYS" -print0 | while IFS= read -r -d '' f; do
        rel=${f#"$SOURCE_DIR"/}
        if rclone check "$SOURCE_DIR" "$DEST" --files-from <(printf '%s\n' "$rel") --one-way -q 2>/dev/null; then
          rm -f -- "$f" && echo "$(date '+%F %T') hapus lokal (sudah di storage): $rel" >> "$LOG"
        fi
      done
      find "$SOURCE_DIR" -mindepth 1 -type d -empty -delete 2>/dev/null || true
    fi
    ;;
  *)
    echo "MODE tidak dikenal: $MODE" >&2; exit 1 ;;
esac
