# 메일서버 실행 가이드 (로컬 Docker Compose)

이 문서는 현재 저장소(ucwareMailServer) 기준으로 메일서버를 실행하는 가장 안전한 순서를 설명합니다.
기본 경로는 저장소 루트(`/workspaces/ucwareMailServer`)입니다.

## 1. 사전 준비

다음 도구가 설치되어 있어야 합니다.

- Docker
- Docker Compose 플러그인 (`docker compose` 명령)

확인 명령:

```bash
docker --version
docker compose version
```

## 2. 저장소로 이동

```bash
cd /workspaces/ucwareMailServer
```

## 3. 환경 변수 파일 준비

1. 예제 파일을 복사합니다.

```bash
cp .env.example .env
```

2. 최소 필수값을 확인/수정합니다.

- `MAIL_DOMAIN`
- `MAIL_HOSTNAME`
- `MAIL_ADMIN_JWT_SECRET` (운영에서는 반드시 강한 랜덤 값)
- `MAIL_ADMIN_EMAIL`
- `MAIL_ADMIN_PASSWORD`

빠르게 점검:

```bash
grep -E 'MAIL_DOMAIN|MAIL_HOSTNAME|MAIL_ADMIN_JWT_SECRET|MAIL_ADMIN_EMAIL|MAIL_ADMIN_PASSWORD' .env
```

## 4. (선택) 기존 컨테이너/볼륨 정리

처음 실행이 아니고 환경을 깨끗하게 재시작하려면 아래를 실행합니다.

```bash
docker compose down -v
```

주의:

- `-v` 옵션은 PostgreSQL 데이터(`archive_db_data`)와 메일 데이터 볼륨을 함께 삭제합니다.

## 5. 메일서버 스택 빌드 및 실행

```bash
docker compose up -d --build
```

실행되는 핵심 서비스:

- `mail-admin` (관리 API)
- `postfix` (SMTP)
- `dovecot` (IMAP/POP3/LMTP)
- `rspamd` + `clamav` (보안 체인)
- `archive-db` (PostgreSQL)
- `roundcube` (웹메일)
- `web-frontend` (분리 웹 UI)

## 6. 기동 상태 확인

1. 컨테이너 상태 확인

```bash
docker compose ps
```

2. 실패 컨테이너 로그 확인

```bash
docker compose logs --tail=200 mail-admin
docker compose logs --tail=200 postfix
docker compose logs --tail=200 dovecot
docker compose logs --tail=200 rspamd
docker compose logs --tail=200 clamav
```

3. API 헬스체크

```bash
curl -fsS http://localhost:8080/healthz
```

## 7. 접속 주소

실행 후 브라우저/클라이언트에서 아래로 접속합니다.

- mail-admin API: `http://localhost:8080`
- 분리 웹 프론트엔드: `http://localhost:8082/login`
- Roundcube: `http://localhost:8081`
- SMTP: `localhost:2525`
- Submission: `localhost:2587`
- IMAP: `localhost:2143`
- IMAPS: `localhost:2993`

기본 관리자 계정(`.env` 기본값 사용 시):

- Email: `admin@example.com`
- Password: `ChangeMeAdmin!123`

## 8. 간단 동작 확인 (선택)

로그인 토큰 발급:

```bash
curl -X POST http://localhost:8080/v1/auth/login \
  -H 'content-type: application/json' \
  -d '{"email":"admin@example.com","password":"ChangeMeAdmin!123"}'
```

응답에서 `accessToken`을 받아 API 호출에 사용합니다.

## 9. mock-icap 프로파일 실행 (선택)

V3 ICAP 모의 테스트가 필요하면 `mock-icap` 프로파일을 함께 올립니다.

```bash
docker compose --profile v3mock up -d --build
```

`mock-icap` 포트:

- `localhost:1344`

## 10. 중지/재시작

중지:

```bash
docker compose down
```

재시작:

```bash
docker compose up -d
```

로그 실시간 보기:

```bash
docker compose logs -f --tail=100
```

## 11. 자주 발생하는 문제와 해결

1. 포트 충돌
- 증상: `Bind for 0.0.0.0:xxxx failed`
- 조치: 해당 포트 사용 프로세스를 중지하거나 `docker-compose.yml` 포트 매핑을 변경

2. 권한 문제(generated/data)
- 증상: 파일 생성 실패 또는 DB open 실패
- 조치: 저장소 루트에서 권한 확인 후 재기동

```bash
sudo chown -R $USER:$USER data generated
```

3. ClamAV 초기 기동 지연
- 증상: rspamd/clamav 관련 헬스 미완료
- 조치: 1~3분 대기 후 로그 재확인

4. 관리자 로그인 실패
- 조치: `.env`의 `MAIL_ADMIN_EMAIL`, `MAIL_ADMIN_PASSWORD` 값 확인 후 컨테이너 재기동

```bash
docker compose up -d --build mail-admin
```

---

운영(실서버) 배포는 [README.md](README.md)와 [scripts/provision-production.sh](scripts/provision-production.sh) 절차를 별도로 따르세요.
