#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_BASE_DIR="${BACKUP_OUT_DIR:-$ROOT_DIR/backups}"
TS="$(date -u +%Y%m%d-%H%M%S)"
BACKUP_DIR="$OUT_BASE_DIR/$TS"
INCLUDE_VMAIL=1
INCLUDE_POSTFIX_SPOOL=1

usage() {
  cat <<'EOF'
Usage: scripts/backup.sh [options]

Options:
  --out-dir <path>            Backup base directory (default: ./backups)
  --skip-vmail                Skip backing up /var/mail/vhosts volume snapshot
  --skip-postfix-spool        Skip backing up /var/spool/postfix volume snapshot
  -h, --help                  Show this help

Output:
  <out-dir>/<timestamp>/
    - app-data.tar.gz
    - generated.tar.gz
    - archive-db.sql.gz
    - vmail.tar.gz (optional)
    - postfix-spool.tar.gz (optional)
    - manifest.txt
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --out-dir)
      OUT_BASE_DIR="$2"
      shift 2
      ;;
    --skip-vmail)
      INCLUDE_VMAIL=0
      shift
      ;;
    --skip-postfix-spool)
      INCLUDE_POSTFIX_SPOOL=0
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

mkdir -p "$BACKUP_DIR"

echo "[backup] root: $ROOT_DIR"
echo "[backup] target: $BACKUP_DIR"

if ! command -v docker >/dev/null 2>&1; then
  echo "docker command not found" >&2
  exit 1
fi

if ! docker compose -f "$ROOT_DIR/docker-compose.yml" ps >/dev/null 2>&1; then
  echo "docker compose is not available for this project" >&2
  exit 1
fi

if [[ -d "$ROOT_DIR/data" ]]; then
  tar czf "$BACKUP_DIR/app-data.tar.gz" -C "$ROOT_DIR" data
  echo "[backup] app-data.tar.gz created"
else
  echo "[backup] data directory not found, skipping"
fi

if [[ -d "$ROOT_DIR/generated" ]]; then
  tar czf "$BACKUP_DIR/generated.tar.gz" -C "$ROOT_DIR" generated
  echo "[backup] generated.tar.gz created"
else
  echo "[backup] generated directory not found, skipping"
fi

if docker compose -f "$ROOT_DIR/docker-compose.yml" ps archive-db | grep -q "archive-db"; then
  docker compose -f "$ROOT_DIR/docker-compose.yml" exec -T archive-db \
    sh -lc 'pg_dump -U "$POSTGRES_USER" "$POSTGRES_DB"' \
    | gzip > "$BACKUP_DIR/archive-db.sql.gz"
  echo "[backup] archive-db.sql.gz created"
else
  echo "[backup] archive-db container is not running, skipping DB dump"
fi

if [[ "$INCLUDE_VMAIL" -eq 1 ]]; then
  if docker compose -f "$ROOT_DIR/docker-compose.yml" ps dovecot | grep -q "dovecot"; then
    docker compose -f "$ROOT_DIR/docker-compose.yml" exec -T dovecot \
      sh -lc 'tar czf - -C /var/mail/vhosts .' > "$BACKUP_DIR/vmail.tar.gz"
    echo "[backup] vmail.tar.gz created"
  else
    echo "[backup] dovecot container is not running, skipping vmail backup"
  fi
fi

if [[ "$INCLUDE_POSTFIX_SPOOL" -eq 1 ]]; then
  if docker compose -f "$ROOT_DIR/docker-compose.yml" ps dovecot | grep -q "dovecot"; then
    docker compose -f "$ROOT_DIR/docker-compose.yml" exec -T dovecot \
      sh -lc 'tar czf - -C /var/spool/postfix .' > "$BACKUP_DIR/postfix-spool.tar.gz"
    echo "[backup] postfix-spool.tar.gz created"
  else
    echo "[backup] dovecot container is not running, skipping postfix spool backup"
  fi
fi

{
  echo "timestamp_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "root_dir=$ROOT_DIR"
  echo "backup_dir=$BACKUP_DIR"
  echo "hostname=$(hostname)"
  echo "git_commit=$(cd "$ROOT_DIR" && git rev-parse --short HEAD 2>/dev/null || true)"
  find "$BACKUP_DIR" -maxdepth 1 -type f -printf '%f\n' | sort
} > "$BACKUP_DIR/manifest.txt"

echo "[backup] completed"
echo "$BACKUP_DIR"
