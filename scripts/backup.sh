#!/usr/bin/env bash
# Nightly pg_dump of the cortex database, uploaded to an S3-compatible bucket.
# Keeps 14 days. Env (also read from /etc/cortex-backup.env if present):
#   BACKUP_S3_ENDPOINT, BACKUP_S3_BUCKET, BACKUP_S3_KEY, BACKUP_S3_SECRET
#   BACKUP_LOCAL_DIR   (testing fallback: store in a local directory instead)
#   BACKUP_DB          (default: cortex)   BACKUP_RETENTION_DAYS (default: 14)
set -euo pipefail

# shellcheck disable=SC1091
[ -f /etc/cortex-backup.env ] && . /etc/cortex-backup.env

DB="${BACKUP_DB:-cortex}"
DAYS="${BACKUP_RETENTION_DAYS:-14}"
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
NAME="cortex-${STAMP}.dump"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

dump() {
  if [ "$(id -un)" = "postgres" ]; then
    pg_dump -Fc "$DB" > "$TMP/$NAME"
  else
    su postgres -c "pg_dump -Fc '$DB'" > "$TMP/$NAME"
  fi
}
dump
[ -s "$TMP/$NAME" ] || { echo "empty dump" >&2; exit 1; }

if [ -n "${BACKUP_LOCAL_DIR:-}" ]; then
  mkdir -p "$BACKUP_LOCAL_DIR"
  cp "$TMP/$NAME" "$BACKUP_LOCAL_DIR/$NAME"
  find "$BACKUP_LOCAL_DIR" -name 'cortex-*.dump' -mtime "+${DAYS}" -delete
  echo "backup: $BACKUP_LOCAL_DIR/$NAME"
  exit 0
fi

for v in BACKUP_S3_ENDPOINT BACKUP_S3_BUCKET BACKUP_S3_KEY BACKUP_S3_SECRET; do
  if [ -z "${!v:-}" ]; then
    echo "$v is not set" >&2; exit 1
  fi
done

export RCLONE_CONFIG_CORTEX_TYPE=s3
export RCLONE_CONFIG_CORTEX_PROVIDER=Other
export RCLONE_CONFIG_CORTEX_ENDPOINT="$BACKUP_S3_ENDPOINT"
export RCLONE_CONFIG_CORTEX_ACCESS_KEY_ID="$BACKUP_S3_KEY"
export RCLONE_CONFIG_CORTEX_SECRET_ACCESS_KEY="$BACKUP_S3_SECRET"

rclone copyto "$TMP/$NAME" "cortex:${BACKUP_S3_BUCKET}/${NAME}"
rclone delete "cortex:${BACKUP_S3_BUCKET}" --min-age "${DAYS}d" --include 'cortex-*.dump'
echo "backup: ${BACKUP_S3_BUCKET}/${NAME}"
