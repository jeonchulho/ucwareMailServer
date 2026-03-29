# 대용량 메일 운영 런북

이 문서는 `docker-compose_large.yml` 예시와 대용량 운영 아키텍처 문서를 바탕으로,
실제 운영 시 점검해야 할 장애 대응/모니터링 포인트를 정리한 런북입니다.

관련 문서:

- [LARGE_SCALE_MAIL_ARCHITECTURE_KO.md](LARGE_SCALE_MAIL_ARCHITECTURE_KO.md)
- [POSTFIX_DOVECOT_SYSTEMD_GUIDE_KO.md](POSTFIX_DOVECOT_SYSTEMD_GUIDE_KO.md)
- [MAIL_SERVER_RUN_GUIDE_KO.md](MAIL_SERVER_RUN_GUIDE_KO.md)
- [docker-compose_large.yml](docker-compose_large.yml)
- [.env.large.example](.env.large.example)

---

## 1) 운영 기본 원칙

1. HTTP 계층과 메일 계층을 같은 방식으로 보지 않는다.
- `web-frontend`, `mail-admin`는 상대적으로 stateless에 가깝게 운영 가능
- Postfix, Dovecot은 큐/세션/저장소 상태를 가진다

2. 장애 전파 방향을 항상 의식한다.
- ClamAV/ICAP 지연 -> Rspamd 지연 -> Postfix 지연 -> SMTP 세션 증가
- Dovecot 저장소 문제 -> 로그인 실패/메일 조회 실패
- DB 장애 -> mail-admin API 장애/아카이브 장애

3. 로그는 단일 서비스가 아니라 체인으로 본다.
- Nginx -> mail-admin -> Postfix -> Rspamd -> ClamAV/ICAP -> Dovecot

---

## 2) 우선 모니터링 대상

### 2.1 HTTP/API

- Nginx upstream 5xx 비율
- `mail-admin` 응답시간 p95/p99
- 로그인 실패율
- `/v1/send` 실패율
- 인스턴스별 healthz 결과

### 2.2 SMTP / 수신 계층

- Postfix active queue 길이
- Postfix deferred queue 길이
- SMTP 동시 세션 수
- 인증 실패율
- 수신 TPS / 발신 TPS

### 2.3 IMAP/POP3 계층

- Dovecot 로그인 성공률
- IMAP 응답시간
- POP3 RETR/TOP 지연
- LMTP 전달 실패율

### 2.4 보안 계층

- Rspamd 평균 검사 시간
- Rspamd timeout 비율
- ClamAV timeout 비율
- ICAP timeout / fail-open 횟수
- 감염 탐지율과 검사 실패율을 분리해서 추적

### 2.5 데이터 계층

- PostgreSQL 연결 수
- 느린 쿼리 수
- 아카이브 저장 실패율
- 메일 저장소 사용량
- inode 사용량

---

## 3) 장애 시나리오별 대응

### 3.1 증상: 로그인은 되는데 메일 발송이 느리다

원인 후보:

- Postfix 큐 적체
- Rspamd 검사 지연
- ClamAV/ICAP timeout
- 원격 수신 서버 전달 지연

우선 점검:

```bash
postqueue -p
postconf -n
```

확인 포인트:

- queue가 급증했는가
- Rspamd backend timeout이 늘었는가
- ClamAV/ICAP 응답이 느린가

조치:

1. 보안 백엔드 지연이 확인되면 fail-open 정책 여부 검토
2. 특정 첨부/도메인에서만 발생하면 샘플 메시지 분석
3. queue가 급증하면 SMTP ingress 일시 제한 검토

### 3.2 증상: SMTP AUTH 실패 증가

원인 후보:

- Dovecot auth 소켓 권한 문제
- mail-admin generated 사용자 파일 미동기화
- 사용자 데이터 불일치

우선 점검:

- Postfix auth 로그
- Dovecot auth 로그
- generated 사용자 파일 최신화 여부

조치:

1. Dovecot auth listener 권한 재확인
2. generated 파일 재생성/배포 경로 점검
3. 중앙 DB/배포 파이프라인 불일치 여부 확인

### 3.3 증상: 수신은 되지만 메일함에서 안 보인다

원인 후보:

- Postfix -> Dovecot LMTP 전달 실패
- Maildir 권한 문제
- Dovecot storage 노드 라우팅 문제

우선 점검:

- `virtual_transport` 경로
- Dovecot LMTP 소켓 접근성
- Maildir 저장 경로 및 UID/GID

조치:

1. LMTP 소켓/스풀 권한 교정
2. Maildir 저장소 권한 복구
3. Dovecot 백엔드 라우팅 확인

### 3.4 증상: 정상 메일이 과도하게 차단된다

원인 후보:

- Rspamd 규칙 과민 설정
- ClamAV 오탐
- ICAP 정책 과도

조치:

1. reject 대신 add header / soft reject 정책 검토
2. 오탐 샘플 별도 분리
3. 신규 룰/버전 반영 직후라면 롤백 고려

### 3.5 증상: 전체적으로 지연되는데 CPU는 낮다

원인 후보:

- 외부 I/O 병목 (DB, storage, DNS, ICAP)
- 큐 락/스토리지 응답 지연
- upstream timeout 대기

조치:

1. CPU보다 I/O wait, 네트워크 지연을 우선 본다
2. DB 연결/쿼리 시간 확인
3. storage latency와 inode 사용률 확인

---

## 4) 배포 전 체크리스트

1. `.env.large.example` 기반 운영 env 분리 여부
2. 중앙 DB/비밀관리 체계 정리 여부
3. Nginx TLS 인증서 준비 여부
4. Postfix/Dovecot 설정 배포 자동화 여부
5. Rspamd/ClamAV/ICAP 장애 정책 문서화 여부
6. 알림 기준(큐 길이, timeout 비율, 5xx 비율) 정의 여부

---

## 5) 실제 예시 파일

- [docker-compose_large.yml](docker-compose_large.yml)
- [deploy/nginx/nginx-mail-admin-scale.conf](deploy/nginx/nginx-mail-admin-scale.conf)
- [docs/diagrams/large-scale-mail-architecture.png](docs/diagrams/large-scale-mail-architecture.png)
