#!/usr/bin/env bash
set -euo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir"

if [[ ! -f .env ]]; then
  cp .env.example .env
fi
source .env

MAIL_DOMAIN="${MAIL_DOMAIN:-example.test}"
MAIL_ADMIN_EMAIL="${MAIL_ADMIN_EMAIL:-admin@example.test}"
MAIL_ADMIN_PASSWORD="${MAIL_ADMIN_PASSWORD:-CiAdminPass1234}"
MAIL_ADMIN_TOTP_CODE="${MAIL_ADMIN_TOTP_CODE:-}"
ANTIVIRUS_V3_ICAP_SERVER="${ANTIVIRUS_V3_ICAP_SERVER:-mock-icap:1344}"
ANTIVIRUS_V3_ICAP_SCHEME="${ANTIVIRUS_V3_ICAP_SCHEME:-respmod}"

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing command: $1"
    exit 1
  fi
}

need_cmd docker
need_cmd curl

nc_cmd=""
if command -v nc >/dev/null 2>&1; then
  nc_cmd="nc"
elif command -v netcat >/dev/null 2>&1; then
  nc_cmd="netcat"
else
  echo "Missing command: nc (or netcat)"
  exit 1
fi

rspamd_av_conf="deploy/docker/rspamd/local.d/antivirus.conf"
backup_file="$(mktemp)"
cp "$rspamd_av_conf" "$backup_file"
cleanup() {
  cp "$backup_file" "$rspamd_av_conf" || true
  rm -f "$backup_file" || true
  docker compose --profile v3mock down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

cat > "$rspamd_av_conf" <<EOF
v3_icap {
  type = "icap";
  servers = "${ANTIVIRUS_V3_ICAP_SERVER}";
  scheme = "${ANTIVIRUS_V3_ICAP_SCHEME}";
  scan_mime_parts = true;
  action = "reject";
  log_clean = false;
  retransmits = 2;
  timeout = 20s;
}
EOF

echo "[1/8] Starting docker compose stack with mock ICAP"
docker compose --profile v3mock up -d --build

echo "[2/8] Waiting for health"
for _ in $(seq 1 60); do
  if curl -fsS http://localhost:8080/healthz >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

echo "[3/8] Logging in"
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
    totp_body="$(cat <<EOF
{"challengeToken":"$challenge","code":"$MAIL_ADMIN_TOTP_CODE"}
EOF
)"
    totp_resp="$(curl -fsS -X POST http://localhost:8080/v1/auth/totp/challenge -H 'content-type: application/json' -d "$totp_body")"
    token="$(printf '%s' "$totp_resp" | sed -n 's/.*"accessToken":"\([^"]*\)".*/\1/p')"
  fi
fi

if [[ -z "$token" ]]; then
  echo "Failed to parse JWT token"
  echo "$login_resp"
  exit 1
fi

auth_header="Authorization: Bearer $token"
test_user="v3-eicar-$(date +%s)@$MAIL_DOMAIN"

echo "[4/8] Creating test mailbox"
create_code="$(curl -sS -o /tmp/v3-eicar-create.out -w '%{http_code}' \
  -X POST http://localhost:8080/v1/users \
  -H "$auth_header" \
  -H 'content-type: application/json' \
  -d '{"email":"'"$test_user"'","password":"StrongPass!123"}')"
[[ "$create_code" == "201" ]]

curl -fsS -X POST -H "$auth_header" http://localhost:8080/v1/sync >/dev/null

echo "[5/8] Sending EICAR mail"
from_addr="probe@$MAIL_DOMAIN"
smtp_payload="$(cat <<EOF
EHLO localhost
MAIL FROM:<$from_addr>
RCPT TO:<$test_user>
DATA
From: <$from_addr>
To: <$test_user>
Subject: V3 ICAP EICAR probe
MIME-Version: 1.0
Content-Type: text/plain; charset=UTF-8

X5O!P%@AP[4\\PZX54(P^)7CC)7}\$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!\$H+H*
.
QUIT
EOF
)"

smtp_out="$(printf '%s\r\n' "$smtp_payload" | "$nc_cmd" -w 15 localhost 2525 || true)"
printf '%s\n' "$smtp_out" > /tmp/v3-eicar-smtp.out

echo "[6/8] Checking mock ICAP was actually called"
if ! docker compose --profile v3mock logs --no-color --since=2m mock-icap | grep -Eiq 'OPTIONS|RESPMOD|REQMOD'; then
  echo "mock-icap did not receive ICAP request"
  docker compose --profile v3mock logs --no-color --tail=200 mock-icap || true
  exit 1
fi

echo "[7/8] Checking rspamd reported antivirus activity"
if ! docker compose --profile v3mock logs --no-color --since=2m rspamd | grep -Eiq 'icap|virus|reject|antivirus'; then
  echo "rspamd did not report antivirus/icap activity"
  docker compose --profile v3mock logs --no-color --tail=200 rspamd || true
  exit 1
fi

echo "[8/8] Cleanup mailbox"
delete_code="$(curl -sS -o /tmp/v3-eicar-delete.out -w '%{http_code}' -X DELETE -H "$auth_header" "http://localhost:8080/v1/users/$test_user")"
[[ "$delete_code" == "200" ]]

echo "V3 mock ICAP security check succeeded"
