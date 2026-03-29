#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

if [[ ! -f .env ]]; then
  cp .env.example .env
fi
source .env

MAIL_DOMAIN="${MAIL_DOMAIN:-example.com}"
MAIL_ADMIN_EMAIL="${MAIL_ADMIN_EMAIL:-admin@example.com}"
MAIL_ADMIN_PASSWORD="${MAIL_ADMIN_PASSWORD:-ChangeMeAdmin!123}"
MAIL_ADMIN_TOTP_CODE="${MAIL_ADMIN_TOTP_CODE:-}"
ANTIVIRUS_PROVIDER="${ANTIVIRUS_PROVIDER:-clamav}"

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing command: $1"
    exit 1
  fi
}

need_cmd docker
need_cmd curl

if [[ "$ANTIVIRUS_PROVIDER" != "clamav" ]]; then
  echo "ANTIVIRUS_PROVIDER=$ANTIVIRUS_PROVIDER"
  echo "EICAR local test currently validates ClamAV chain only. Skipping."
  exit 0
fi

nc_cmd=""
if command -v nc >/dev/null 2>&1; then
  nc_cmd="nc"
elif command -v netcat >/dev/null 2>&1; then
  nc_cmd="netcat"
else
  echo "Missing command: nc (or netcat)"
  exit 1
fi

echo "[1/9] Starting docker compose stack"
docker compose up -d --build

echo "[2/9] Waiting for mail-admin health"
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

echo "[3/9] Logging in"
login_body="$(cat <<EOF
{"email":"$MAIL_ADMIN_EMAIL","password":"$MAIL_ADMIN_PASSWORD"}
EOF
)"
login_resp="$(curl -fsS -X POST http://localhost:8080/v1/auth/login -H 'content-type: application/json' -d "$login_body")"
token="$(printf '%s' "$login_resp" | sed -n 's/.*"accessToken":"\([^"]*\)".*/\1/p')"

if [[ -z "$token" ]]; then
  challenge="$(printf '%s' "$login_resp" | sed -n 's/.*"challengeToken":"\([^"]*\)".*/\1/p')"
  status="$(printf '%s' "$login_resp" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')"
  if [[ "$status" == "totp_required" && -n "$challenge" ]]; then
    if [[ -z "$MAIL_ADMIN_TOTP_CODE" ]]; then
      echo "MAIL_ADMIN_TOTP_CODE is required when TOTP is enabled"
      exit 1
    fi
    echo "[3.1/9] Completing TOTP challenge"
    totp_body="$(cat <<EOF
{"challengeToken":"$challenge","code":"$MAIL_ADMIN_TOTP_CODE"}
EOF
)"
    totp_resp="$(curl -fsS -X POST http://localhost:8080/v1/auth/totp/challenge -H 'content-type: application/json' -d "$totp_body")"
    token="$(printf '%s' "$totp_resp" | sed -n 's/.*"accessToken":"\([^"]*\)".*/\1/p')"
  fi
fi

if [[ -z "$token" ]]; then
  echo "Failed to parse JWT token from login response"
  echo "$login_resp"
  exit 1
fi

auth_header="Authorization: Bearer $token"
test_user="eicar-$(date +%s)@$MAIL_DOMAIN"

echo "[4/9] Creating target mailbox and syncing maps"
create_code="$(curl -sS -o /tmp/eicar-create.out -w '%{http_code}' \
  -X POST http://localhost:8080/v1/users \
  -H "$auth_header" \
  -H 'content-type: application/json' \
  -d '{"email":"'"$test_user"'","password":"StrongPass!123"}')"
[[ "$create_code" == "201" ]]

curl -fsS -X POST -H "$auth_header" http://localhost:8080/v1/sync >/dev/null

echo "[5/9] Sending EICAR test mail via SMTP"
from_addr="probe@$MAIL_DOMAIN"
subject="EICAR antivirus probe"

smtp_payload="$(cat <<EOF
EHLO localhost
MAIL FROM:<$from_addr>
RCPT TO:<$test_user>
DATA
From: <$from_addr>
To: <$test_user>
Subject: $subject
MIME-Version: 1.0
Content-Type: text/plain; charset=UTF-8

X5O!P%@AP[4\\PZX54(P^)7CC)7}\$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!\$H+H*
.
QUIT
EOF
)"

smtp_out="$(printf '%s\r\n' "$smtp_payload" | "$nc_cmd" -w 15 localhost 2525 || true)"
printf '%s\n' "$smtp_out" > /tmp/eicar-smtp.out

if printf '%s' "$smtp_out" | grep -Eq '(^|[[:space:]])5[0-9]{2}[[:space:]]'; then
  echo "SMTP rejection observed (expected with strict policy)"
else
  echo "No immediate SMTP reject observed; checking rspamd logs for virus hit"
fi

echo "[6/9] Checking rspamd logs for EICAR/ClamAV hits"
log_hit="no"
if docker compose logs --no-color --since=2m rspamd 2>/dev/null | grep -Eiq 'eicar|clam|virus|reject'; then
  log_hit="yes"
fi

if [[ "$log_hit" != "yes" ]]; then
  echo "Did not find EICAR/virus hit in rspamd logs."
  echo "---- last SMTP transcript ----"
  cat /tmp/eicar-smtp.out || true
  echo "---- rspamd logs ----"
  docker compose logs --no-color --tail=200 rspamd || true
  exit 1
fi

echo "[7/9] Querying audit logs"
curl -fsS -H "$auth_header" 'http://localhost:8080/v1/audits?limit=50' >/tmp/eicar-audits.json

echo "[8/9] Cleanup mailbox"
delete_code="$(curl -sS -o /tmp/eicar-delete.out -w '%{http_code}' -X DELETE -H "$auth_header" "http://localhost:8080/v1/users/$test_user")"
[[ "$delete_code" == "200" ]]

echo "[9/9] Done"
echo "EICAR security check succeeded"
