#!/usr/bin/env bash
# Logical backup of the Crossdock database (spec 13.5). Run from a systemd timer.
#
#   COMPOSE   compose command and file   (default: docker compose -f deploy/docker-compose.prod.yml)
#   BACKUP_DIR  where dumps are kept     (default: backups)
#   BACKUP_S3_URI  optional, e.g. s3://bucket/crossdock; needs the aws CLI and its credentials
#   KEEP_DAYS  local retention           (default: 14)
set -euo pipefail

COMPOSE=${COMPOSE:-docker compose -f deploy/docker-compose.prod.yml}
DIR=${BACKUP_DIR:-backups}
KEEP=${KEEP_DAYS:-14}
mkdir -p "$DIR"
out="$DIR/crossdock-$(date -u +%Y%m%dT%H%M%SZ).dump"

# Custom format: compressed, and pg_restore can restore one table or the whole file.
$COMPOSE exec -T db pg_dump -U crossdock -d crossdock -Fc > "$out.partial"
[ -s "$out.partial" ] || { echo "backup is empty, keeping nothing" >&2; rm -f "$out.partial"; exit 1; }
mv "$out.partial" "$out"
echo "backup written: $out ($(wc -c < "$out") bytes)"

if [ -n "${BACKUP_S3_URI:-}" ]; then
  aws s3 cp "$out" "$BACKUP_S3_URI/$(basename "$out")"
fi
find "$DIR" -name 'crossdock-*.dump' -mtime +"$KEEP" -delete
