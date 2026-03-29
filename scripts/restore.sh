#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKUP_DIR=""
NO_RESTART=0

usage() {
  cat <<'EOF'
Usage: scripts/restore.sh --backup-dir <path> [options]

Options:
  --backup-dir <path>         Backup directory created by scripts/backup.sh
  --no-restart                Do not restart compose services before/after restore
  -h, --help                  Show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --backup-dir)
      BACKUP_DIR="$2"
      shift 2
      ;;
    --no-restart)
      NO_RESTART=1
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

if [[ -z "$BACKUP_DIR" ]]; then
  echo "--backup-dir is required" >&2
  usage
  exit 1
fi

if [[ ! -d "$BACKUP_DIR" ]]; then
  echo "backup directory does not exist: $BACKUP_DIR" >&2
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "docker command not found" >&2
  exit 1
fi

if [[ "$NO_RESTART" -eq 0 ]]; then
  echo "[restore] stopping services"
  docker compose -f "$ROOT_DIR/docker-compose.yml" down
  echo "[restore] starting required services"
  docker compose -f "$ROOT_DIR/docker-compose.yml" up -d archive-db mail-admin dovecot postfix
fi

if [[ -f "$BACKUP_DIR/app-data.tar.gz" ]]; then
  echo "[restore] restoring app data"
  rm -rf "$ROOT_DIR/data"
  tar xzf "$BACKUP_DIR/app-data.tar.gz" -C "$ROOT_DIR"
fi

if [[ -f "$BACKUP_DIR/generated.tar.gz" ]]; then
  echo "[restore] restoring generated files"
  rm -rf "$ROOT_DIR/generated"
  tar xzf "$BACKUP_DIR/generated.tar.gz" -C "$ROOT_DIR"
fi

if [[ -f "$BACKUP_DIR/archive-db.sql.gz" ]]; then
  echo "[restore] restoring archive db"
  gunzip -c "$BACKUP_DIR/archive-db.sql.gz" \
    | docker compose -f "$ROOT_DIR/docker-compose.yml" exec -T archive-db \
      sh -lc 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"'
fi

if [[ -f "$BACKUP_DIR/vmail.tar.gz" ]]; then
  echo "[restore] restoring vmail"
  docker compose -f "$ROOT_DIR/docker-compose.yml" exec -T dovecot sh -lc 'rm -rf /var/mail/vhosts/*'
  docker compose -f "$ROOT_DIR/docker-compose.yml" exec -T dovecot sh -lc 'tar xzf - -C /var/mail/vhosts' < "$BACKUP_DIR/vmail.tar.gz"
fi

if [[ -f "$BACKUP_DIR/postfix-spool.tar.gz" ]]; then
  echo "[restore] restoring postfix spool"
  docker compose -f "$ROOT_DIR/docker-compose.yml" exec -T dovecot sh -lc 'rm -rf /var/spool/postfix/*'
  docker compose -f "$ROOT_DIR/docker-compose.yml" exec -T dovecot sh -lc 'tar xzf - -C /var/spool/postfix' < "$BACKUP_DIR/postfix-spool.tar.gz"
fi

if [[ "$NO_RESTART" -eq 0 ]]; then
  echo "[restore] restarting services"
  docker compose -f "$ROOT_DIR/docker-compose.yml" up -d --build
fi

echo "[restore] completed"
