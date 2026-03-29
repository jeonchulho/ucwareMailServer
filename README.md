# ucwareMailServer

Postfix + Dovecot 기반 메일 서버를 직접 운영하면서, Go API로 메일 계정/매핑 파일을 관리하는 프로젝트입니다.

## 가이드 문서

처음 시작할 때는 아래 순서로 문서를 보면 가장 빠르게 전체 구조를 이해할 수 있습니다.

1. [MAIL_SERVER_RUN_GUIDE_KO.md](MAIL_SERVER_RUN_GUIDE_KO.md): 로컬 실행 절차(준비 -> 실행 -> 점검)
2. [POSTFIX_DOVECOT_SYSTEMD_GUIDE_KO.md](POSTFIX_DOVECOT_SYSTEMD_GUIDE_KO.md): Postfix/Dovecot/Rspamd/ICAP/systemd 운영 레퍼런스
3. [LARGE_SCALE_MAIL_ARCHITECTURE_KO.md](LARGE_SCALE_MAIL_ARCHITECTURE_KO.md): Nginx 기반 멀티 인스턴스, 메일 계층 분리, 대용량 운영 전환 가이드

대용량 예시 구성을 바로 보려면 아래 파일도 참고하세요.

- [docker-compose_large.yml](docker-compose_large.yml)

## 아키텍처

- Postfix: SMTP 수신/발신
- Dovecot: IMAP + LMTP 전달
- Rspamd: 스팸 분석 + milter 연동 (ClamAV 바이러스 검사와 연결)
- Roundcube: 웹메일 UI(로컬 통합 환경 포함)
- mail-admin(Go): JWT/RBAC API + 계정 CRUD + 감사 로그 + 매핑 파일 생성

mail-admin이 생성하는 파일:

- Dovecot 비밀번호 파일: `generated/dovecot/users.passwd`
- Postfix 메일박스 매핑: `generated/postfix/virtual_mailbox_maps`
- Postfix 도메인 목록: `generated/postfix/virtual_mailbox_domains`

## 빠른 시작 (Docker Compose)

```bash
cp .env.example .env
docker compose up -d --build
```

기본 `.env.example` 기준으로 아래가 함께 활성화됩니다.

- SSR 웹 UI: `http://localhost:8080/login`
- Archive DB(PostgreSQL): `archive-db` 컨테이너 (메일 목록/상세/작성 저장용)

기본 compose 구성은 아래 체인으로 동작합니다.

- Postfix -> Rspamd milter(11332)
- Rspamd -> ClamAV(3310)
- 악성 첨부 탐지 시 Rspamd 정책에 따라 reject

접속 포트:

- mail-admin API: `http://localhost:8080`
- 분리 웹 프런트엔드(gin, 선택): `http://localhost:8082`
- Roundcube: `http://localhost:8081`
- SMTP: `localhost:2525`
- Submission: `localhost:2587`
- IMAP: `localhost:2143`
- IMAPS: `localhost:2993`

## 1-1) 분리 웹 서버 사용 (선택)

기존 `mail-admin` 내장 SSR 대신 별도 Go 웹 서버(`gin`)를 사용할 수 있습니다.

```bash
docker compose up -d --build web-frontend mail-admin archive-db
```

접속:

- `http://localhost:8082/login`

특징:

- 웹 서버 프로세스가 `mail-admin` API와 분리되어 독립 배포 가능
- 웹 서버는 `API_BASE_URL`을 통해 `mail-admin`의 `/v1` API를 호출

## 1) 웹 UI 사용 (권장)

1. 브라우저에서 `http://localhost:8082/login` 접속
2. 기본 관리자 계정으로 로그인

```text
email: admin@example.com
password: ChangeMeAdmin!123
```

3. 좌측 폴더에서 메일 목록 확인
4. `편지쓰기`로 메시지 작성 후 `보낸편지함`에서 확인

참고:

- 웹 화면은 Archive DB를 사용하므로 compose 기본값(archive-db 포함)으로 실행하는 것을 권장합니다.

## 2) Go mail-admin 실행 (JWT/RBAC)

```bash
go mod tidy
JWT_SECRET=change-this-jwt-secret \
BOOTSTRAP_ADMIN_EMAIL=admin@example.com \
BOOTSTRAP_ADMIN_PASSWORD=ChangeMeAdmin!123 \
go run ./cmd/mail-admin
```

기본 포트는 `:8080`입니다.

## 3) (선택) API 로그인 후 토큰 발급

SSR 웹 UI만 사용하면 토큰 발급은 필요 없습니다.

```bash
curl -X POST http://localhost:8080/v1/auth/login \
	-H 'content-type: application/json' \
	-d '{"email":"admin@example.com","password":"ChangeMeAdmin!123"}'
```

응답의 `accessToken`을 `Authorization: Bearer <token>`으로 사용합니다.

## 4) 계정 API

계정 생성:

```bash
TOKEN='<로그인으로 받은 JWT>'

curl -X POST http://localhost:8080/v1/users \
	-H "Authorization: Bearer $TOKEN" \
	-H 'content-type: application/json' \
	-d '{"email":"alice@example.com","password":"StrongPass!123"}'
```

계정 목록:

```bash
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/v1/users
```

계정 삭제:

```bash
curl -X DELETE \
	-H "Authorization: Bearer $TOKEN" \
	http://localhost:8080/v1/users/alice@example.com
```

파일 재동기화:

```bash
curl -X POST \
	-H "Authorization: Bearer $TOKEN" \
	http://localhost:8080/v1/sync
```

감사 로그 조회:

```bash
curl -H "Authorization: Bearer $TOKEN" \
	'http://localhost:8080/v1/audits?limit=100'
```

권한 정책:

- `admin`: 사용자 생성/삭제/조회, 동기화, 감사 로그 조회
- `operator`: 사용자 조회, 동기화, 감사 로그 조회
- `viewer`: 사용자 조회

## 4-1) 메일박스/메시지 아카이브 API

Docker Compose 기본값에서 Archive DB가 이미 활성화되어 있습니다.
직접 실행 시에만 아래 환경변수를 설정하세요.

```bash
export ARCHIVE_DB_ENABLED=true
export ARCHIVE_DB_DRIVER=postgres
export ARCHIVE_DSN='postgres://user:pass@localhost:5432/mailarchive?sslmode=disable'
```

메일박스 생성:

```bash
curl -X POST http://localhost:8080/v1/mailboxes \
	-H "Authorization: Bearer $TOKEN" \
	-H 'content-type: application/json' \
	-d '{"userEmail":"alice@example.com","name":"INBOX"}'
```

메일 저장(수신/발신):

```bash
curl -X POST http://localhost:8080/v1/messages \
	-H "Authorization: Bearer $TOKEN" \
	-H 'content-type: application/json' \
	-d '{
		"mailboxId":"<mailbox-id>",
		"direction":"inbound",
		"fromAddr":"sender@external.com",
		"toAddr":"alice@example.com",
		"subject":"hello",
		"rawMime":"From: sender@external.com\nTo: alice@example.com\nSubject: hello\n\nbody",
		"textBody":"body"
	}'
```

메일 조회:

```bash
curl -H "Authorization: Bearer $TOKEN" \
	'http://localhost:8080/v1/messages?mailboxId=<mailbox-id>&limit=100'
```

## 5) Postfix/Dovecot 설정 반영

예시 파일:

- Postfix: `deploy/postfix/main.cf.example`
- Dovecot: `deploy/dovecot/dovecot.conf.example`

운영 서버에서 다음 위치로 복사/적용하세요.

- `/etc/postfix/main.cf`
- `/etc/dovecot/dovecot.conf`

그리고 mail-admin이 만든 파일을 각각 연결합니다.

- `/etc/postfix/virtual_mailbox_maps`
- `/etc/postfix/virtual_mailbox_domains`
- `/etc/dovecot/users.passwd`

Postfix map DB 생성:

```bash
postmap /etc/postfix/virtual_mailbox_maps
postmap /etc/postfix/virtual_mailbox_domains
```

서비스 재시작:

```bash
systemctl restart postfix
systemctl restart dovecot
```

## 6) DNS 필수 설정

- MX: `example.com -> mail.example.com`
- A: `mail.example.com -> 서버 IP`
- PTR: 서버 IP -> `mail.example.com`
- SPF: `v=spf1 mx -all`
- DKIM, DMARC는 운영 단계에서 반드시 추가

DNS 점검 스크립트:

```bash
chmod +x scripts/check-dns.sh
./scripts/check-dns.sh example.com mail.example.com 203.0.113.10
```

## 7) 운영 자동화 스크립트

Ubuntu 서버에서 root로 실행:

```bash
chmod +x scripts/provision-production.sh
sudo ./scripts/provision-production.sh example.com mail.example.com admin@example.com 203.0.113.10
```

외부 백신(V3 등) 옵션 사용 예시:

```bash
export ANTIVIRUS_PROVIDER=v3
export ANTIVIRUS_V3_ICAP_SERVER=10.0.0.5:1344
export ANTIVIRUS_V3_ICAP_SCHEME=respmod
export ANTIVIRUS_FAIL_OPEN=yes
sudo ./scripts/provision-production.sh example.com mail.example.com admin@example.com 203.0.113.10

## 8) 백업/복구 자동화

P0 운영 항목으로 백업/복구 스크립트를 제공합니다.

```bash
chmod +x scripts/backup.sh scripts/restore.sh
./scripts/backup.sh
```

복구:

```bash
./scripts/restore.sh --backup-dir backups/<timestamp>
```

상세 절차(리허설 포함):

- `BACKUP_RESTORE_RUNBOOK.md`

## 9) 스케줄 헬스체크/알림 (GitHub Actions)

운영 URL 감시를 위해 10분 주기 헬스체크 워크플로우를 제공합니다.

- 워크플로우: `.github/workflows/uptime-healthcheck.yml`

필수 GitHub Secret:

- `UPTIME_CHECK_URL`: 예) `https://mail-admin.example.com/healthz`

선택 GitHub Secret:

- `ALERT_WEBHOOK_URL`: 실패 시 알림 전송(Webhook endpoint)

선택 GitHub Variables:

- `UPTIME_EXPECTED_STATUS` (기본: `200`)
- `UPTIME_EXPECTED_BODY_SUBSTRING` (응답 본문 검증 필요 시)

수동 실행:

1. GitHub Actions에서 `Uptime Healthcheck` 선택
2. 필요하면 `override_url` 입력 후 실행
```

안티바이러스 비활성화 예시:

```bash
export ANTIVIRUS_PROVIDER=none
sudo ./scripts/provision-production.sh example.com mail.example.com admin@example.com 203.0.113.10
```

이 스크립트가 하는 일:

- Postfix/Dovecot/Rspamd/OpenDKIM/Certbot 설치
- Let's Encrypt 인증서 발급
- DKIM 키 생성 및 OpenDKIM 구성
- Postfix milter(OpenDKIM) 연동
- DNS 레코드 가이드 파일 생성: `/root/mail-dns/dns-<domain>.txt`
- 안티바이러스 제공자 옵션 처리: `clamav | v3(icap) | none`

## 8) 로컬 E2E 테스트

```bash
chmod +x scripts/e2e-local.sh
./scripts/e2e-local.sh
```

검증 항목:

- JWT 로그인
- 사용자 생성/조회/삭제
- 매핑 파일 생성 확인
- 동기화 API
- 감사 로그 조회

## 8-1) 스팸/바이러스(EICAR) 보안 검증

Rspamd + ClamAV 경로가 실제로 동작하는지 EICAR 표준 테스트 문자열로 검증합니다.

```bash
chmod +x scripts/e2e-security-eicar.sh
./scripts/e2e-security-eicar.sh
```

관리자 계정에 TOTP가 활성화된 경우:

```bash
MAIL_ADMIN_TOTP_CODE=123456 ./scripts/e2e-security-eicar.sh
```

검증 성공 기준:

- SMTP 단계에서 5xx 거부 응답이 나오거나,
- Rspamd 로그에서 EICAR/ClamAV 탐지 흔적이 확인됨

## 8-2) 외부 백신(V3/ICAP) 경로 보안 검증

외부 백신 연동 경로는 mock ICAP 서버로 로컬/CI에서 검증할 수 있습니다.

```bash
chmod +x scripts/e2e-security-v3-mock.sh
ANTIVIRUS_PROVIDER=v3 \
ANTIVIRUS_V3_ICAP_SERVER=mock-icap:1344 \
./scripts/e2e-security-v3-mock.sh
```

검증 성공 기준:

- mock-icap 컨테이너 로그에 ICAP 요청(OPTIONS/RESPMOD/REQMOD) 흔적 존재
- rspamd 로그에 icap/antivirus 처리 흔적 존재

## 스팸/바이러스 검사 구성

- 로컬(Docker): `postfix -> rspamd -> clamav`
- 운영(provision script): `postfix milters = rspamd + opendkim`
	- `ANTIVIRUS_PROVIDER=clamav`: `rspamd antivirus -> local clamd`
	- `ANTIVIRUS_PROVIDER=v3`: `rspamd antivirus -> external ICAP`
	- `ANTIVIRUS_PROVIDER=none`: 바이러스 검사 비활성화

관련 파일:

- Postfix milter 설정: `deploy/docker/postfix/main.cf`
- Rspamd 로컬 설정: `deploy/docker/rspamd/local.d/worker-proxy.inc`
- Rspamd ClamAV 모듈: `deploy/docker/rspamd/local.d/antivirus.conf`
- 운영 자동화: `scripts/provision-production.sh`

## 9) OAuth2 소셜 로그인 (Google / Microsoft)

### Google 설정
1. [Google Cloud Console](https://console.cloud.google.com/apis/credentials) → OAuth 2.0 클라이언트 ID 생성
2. 승인된 리디렉션 URI에 `<OAUTH2_CALLBACK_BASE>/v1/auth/oauth2/google/callback` 추가
3. `.env`에 `GOOGLE_OAUTH2_CLIENT_ID`, `GOOGLE_OAUTH2_CLIENT_SECRET` 설정

### Microsoft 설정
1. [Azure 앱 등록](https://portal.azure.com/#blade/Microsoft_AAD_RegisteredApps) → 신규 등록
2. 리디렉션 URI: `<OAUTH2_CALLBACK_BASE>/v1/auth/oauth2/microsoft/callback`
3. API 권한: `openid`, `email`, `profile`
4. `.env`에 `MICROSOFT_OAUTH2_CLIENT_ID`, `MICROSOFT_OAUTH2_CLIENT_SECRET` 설정
5. 단일 테넌트이면 `MICROSOFT_TENANT=<테넌트 ID>`, 다중 테넌트면 `common`

### OAuth2 로그인 흐름
```
브라우저 → GET /v1/auth/oauth2/google
				← 302 → Google 로그인
				→ GET /v1/auth/oauth2/google/callback?code=...&state=...
				← 200 { accessToken, refreshToken, role, email }
```

처음 로그인하는 OAuth2 계정은 `viewer` 권한으로 생성됩니다.  
이후 admin이 `PATCH /v1/auth/admins/{email}/role`로 권한을 상향할 수 있습니다.

## 10) TOTP 2단계 인증

### TOTP 설정 (관리자 본인)
```bash
# 1. 비밀키 생성 (응답의 otpAuthURL을 Google Authenticator 등에 QR로 등록)
curl -X POST http://localhost:8080/v1/auth/totp/setup \
	-H "Authorization: Bearer $TOKEN"

# 2. OTP 앱에서 확인한 6자리 코드로 활성화
curl -X POST http://localhost:8080/v1/auth/totp/confirm \
	-H "Authorization: Bearer $TOKEN" \
	-H 'content-type: application/json' \
	-d '{"code":"123456"}'
```

### TOTP 로그인 흐름 (활성화 후)
```bash
# 1단계: 비밀번호 로그인 → totp_required 응답
RESP=$(curl -s -X POST http://localhost:8080/v1/auth/login \
	-H 'content-type: application/json' \
	-d '{"email":"admin@example.com","password":"..."}')
# {"status":"totp_required","challengeToken":"<token>"}

CHALLENGE=$(echo $RESP | jq -r .challengeToken)

# 2단계: TOTP 코드 + challenge token → 최종 JWT
curl -X POST http://localhost:8080/v1/auth/totp/challenge \
	-H 'content-type: application/json' \
	-d "{\"challengeToken\":\"$CHALLENGE\",\"code\":\"123456\"}"
```

### TOTP 비활성화
```bash
curl -X POST http://localhost:8080/v1/auth/totp/disable \
	-H "Authorization: Bearer $TOKEN" \
	-H 'content-type: application/json' \
	-d '{"password":"현재비밀번호"}'
```

## 환경 변수

- `ADDR` (기본 `:8080`)
- `JWT_SECRET` (필수)
- `JWT_ISSUER` (기본 `ucware-mail-admin`)
- `JWT_EXPIRY_MINUTES` (기본 `60`)
- `BOOTSTRAP_ADMIN_EMAIL` (기본 `admin@example.com`)
- `BOOTSTRAP_ADMIN_PASSWORD` (기본 `ChangeMeAdmin!123`)
- `BOOTSTRAP_ADMIN_ROLE` (기본 `admin`, 값: `admin|operator|viewer`)
- `DB_PATH` (기본 `./data/mailadmin.db`)
- `DOVECOT_USERS_FILE` (기본 `./generated/dovecot/users.passwd`)
- `POSTFIX_MAILBOX_MAPS_FILE` (기본 `./generated/postfix/virtual_mailbox_maps`)
- `POSTFIX_DOMAINS_FILE` (기본 `./generated/postfix/virtual_mailbox_domains`)
- `MAIL_ROOT` (기본 `/var/mail/vhosts`)
- `MAIL_UID` (기본 `5000`)
- `MAIL_GID` (기본 `5000`)
- `BCRYPT_COST` (기본 Go bcrypt 기본값)
- `OAUTH2_CALLBACK_BASE` (기본 `http://localhost:8080`)
- `GOOGLE_OAUTH2_CLIENT_ID` / `GOOGLE_OAUTH2_CLIENT_SECRET`
- `MICROSOFT_OAUTH2_CLIENT_ID` / `MICROSOFT_OAUTH2_CLIENT_SECRET`
- `MICROSOFT_TENANT` (기본 `common`)
- `TOTP_ISSUER` (기본 `ucware-mail-admin`)
- `TOTP_CHALLENGE_EXPIRY_MINUTES` (기본 `5`)
- `ANTIVIRUS_PROVIDER` (기본 `clamav`, 값: `clamav|v3|none`)
- `ANTIVIRUS_FAIL_OPEN` (기본 `yes`, 값: `yes|no`)
- `ANTIVIRUS_V3_ICAP_SERVER` (`ANTIVIRUS_PROVIDER=v3`일 때 필수)
- `ANTIVIRUS_V3_ICAP_SCHEME` (기본 `respmod`)
- `ARCHIVE_DB_ENABLED` (기본 `false`)
- `ARCHIVE_DB_DRIVER` (기본 `postgres`, 값: `postgres|mysql|oracle`)
- `ARCHIVE_DSN` (`ARCHIVE_DB_ENABLED=true`일 때 필수)

## 주의 사항

- 이 저장소는 Gmail급 전체 기능이 아니라 self-hosted 메일 서버의 실전 시작점입니다.
- 운영 환경에서는 fail2ban, 백업, 모니터링, 로그 중앙화(ELK/Loki 등)를 추가하세요.
- Docker Compose 설정은 로컬 검증용입니다. 운영 환경에서는 TLS 인증서, 방화벽, 백업 정책을 반드시 별도로 구성하세요.
- 기본 부트스트랩 관리자 비밀번호는 반드시 변경하세요.