#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

if [[ ! -f .env ]]; then
  cp .env.example .env
fi

source .env

MAIL_ADMIN_EMAIL="${MAIL_ADMIN_EMAIL:-admin@example.com}"
MAIL_ADMIN_PASSWORD="${MAIL_ADMIN_PASSWORD:-ChangeMeAdmin!123}"

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing command: $1"
    exit 1
  fi
}

need_cmd docker
need_cmd curl

echo "[1/7] Starting docker compose stack"
docker compose up -d --build

echo "[2/7] Waiting for mail-admin health"
healthy="no"
for _ in $(seq 1 60); do
  if curl -fsS http://localhost:8080/healthz >/dev/null 2>&1; then
    healthy="yes"
    break
  fi
  sleep 2
done
if [[ "$healthy" != "yes" ]]; then
  echo "mail-admin health check failed"
  docker compose ps
  docker compose logs --no-color --tail=120 mail-admin || true
  exit 1
fi

echo "[3/7] Logging in to get JWT"
login_body="$(cat <<EOF
{"email":"$MAIL_ADMIN_EMAIL","password":"$MAIL_ADMIN_PASSWORD"}
EOF
)"
login_resp="$(curl -fsS -X POST http://localhost:8080/v1/auth/login -H 'content-type: application/json' -d "$login_body")"
token="$(printf '%s' "$login_resp" | sed -n 's/.*"accessToken":"\([^"]*\)".*/\1/p')"

if [[ -z "$token" ]]; then
  echo "Failed to parse JWT token from login response"
  echo "$login_resp"
  exit 1
fi

auth_header="Authorization: Bearer $token"
test_user="e2e-user@example.com"

echo "[4/7] Creating user"
create_code="$(curl -sS -o /tmp/e2e-create.out -w '%{http_code}' \
  -X POST http://localhost:8080/v1/users \
  -H "$auth_header" \
  -H 'content-type: application/json' \
  -d '{"email":"'"$test_user"'","password":"StrongPass!123"}')"
[[ "$create_code" == "201" ]]

echo "[5/7] Listing users and forcing sync"
curl -fsS -H "$auth_header" http://localhost:8080/v1/users | grep -q "$test_user"
curl -fsS -X POST -H "$auth_header" http://localhost:8080/v1/sync >/dev/null

echo "[6/7] Verifying generated mapping files"
docker compose exec -T postfix sh -lc "grep -q '$test_user' /etc/postfix/generated/virtual_mailbox_maps"
docker compose exec -T postfix sh -lc "grep -q 'example.com' /etc/postfix/generated/virtual_mailbox_domains"
docker compose exec -T dovecot sh -lc "grep -q '$test_user' /etc/dovecot/generated/users.passwd"

echo "[7/7] Checking audits and deleting user"
audits_json="$(curl -fsS -H "$auth_header" "http://localhost:8080/v1/audits?limit=20")"
echo "$audits_json" | grep -q '"action"'
delete_code="$(curl -sS -o /tmp/e2e-delete.out -w '%{http_code}' -X DELETE -H "$auth_header" "http://localhost:8080/v1/users/$test_user")"
[[ "$delete_code" == "200" ]]

echo "E2E succeeded"
