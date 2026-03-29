# Postfix, Dovecot, systemd 상세 가이드

이 문서는 메일 서버 운영에서 자주 함께 쓰이는 5가지 구성요소를 설명합니다.

- Postfix: 메일 전송(MTA)
- Dovecot: 메일 저장소 접근/인증(IMAP/POP3/LMTP)
- systemd: 리눅스 서비스 관리자
- ICAP: 외부 콘텐츠 검사 프로토콜(백신/보안 게이트웨이 연동)
- Rspamd: 고성능 스팸/보안 정책 엔진(milter)

아래 내용은 개념 설명뿐 아니라 실제 운영에서 자주 문제 되는 포인트를 중심으로 정리했습니다.

---

## 1) Postfix

### 1.1 제품 개요

Postfix는 대표적인 오픈소스 MTA(Mail Transfer Agent)입니다.
주요 역할은 다음과 같습니다.

- 외부/내부 SMTP 연결 수신
- 릴레이 정책 적용(누가 어디로 보낼 수 있는지)
- 큐잉(즉시 전송 실패 시 재시도)
- 로컬 전달 또는 외부 서버 전달
- milter(Rspamd/OpenDKIM 등) 연동

쉽게 말해, "메일을 받아서 정책 검증 후 적절한 목적지로 전달하는 전송 엔진"입니다.

### 1.2 핵심 기능

- SMTP/Submission 포트 운영
- SASL 인증 연동(Dovecot auth 등)
- TLS 적용(STARTTLS/SMTPS 정책)
- 가상 도메인/메일박스 매핑
- 큐 관리 및 재시도(backoff)
- 콘텐츠 필터/milter 연동

### 1.3 실무에서 중요한 설정 항목

- `myhostname`, `mydomain`, `myorigin`
- `inet_interfaces`, `inet_protocols`
- `mynetworks`
- `smtpd_recipient_restrictions`
- `virtual_mailbox_domains`, `virtual_mailbox_maps`, `virtual_transport`
- `smtpd_sasl_auth_enable`, `smtpd_sasl_type`, `smtpd_sasl_path`
- `smtpd_tls_*`, `smtp_tls_*`
- `smtpd_milters`, `non_smtpd_milters`

### 1.4 주의해야 할 점

1. 오픈 릴레이 방지
- `permit_mynetworks`와 `permit_sasl_authenticated`의 순서/조합이 잘못되면 오픈 릴레이 위험이 생깁니다.
- `reject_unauth_destination`를 반드시 포함하세요.

2. TLS 정책 오해
- 개발에서 `smtpd_tls_security_level=may`를 쓰더라도 운영에서는 인증서/강한 정책 적용이 필요합니다.
- 인증서 경로, 체인(fullchain) 유효성을 주기적으로 점검하세요.

3. milter 장애 전파
- Rspamd/ClamAV 장애 시 메일 수신 전체가 지연/실패할 수 있습니다.
- `milter_default_action`(accept/tempfail) 정책을 서비스 특성에 맞게 정하세요.

4. 매핑 파일 갱신 누락
- `virtual_mailbox_maps`, `virtual_mailbox_domains` 변경 후 `postmap` 갱신 누락 시 반영되지 않습니다.
- 자동화 스크립트 또는 서비스 시작 루틴에서 갱신을 보장하세요.

5. 큐 누적 모니터링
- 외부 DNS/상대 서버 문제로 큐가 급증할 수 있습니다.
- 큐 길이 모니터링과 경보 기준을 운영 초기에 정하세요.

### 1.5 자주 쓰는 점검 명령

```bash
postfix check
postqueue -p
postconf -n
postmap /etc/postfix/virtual_mailbox_maps
postmap /etc/postfix/virtual_mailbox_domains
```

---

## 2) Dovecot

### 2.1 제품 개요

Dovecot은 메일박스 접근/인증에 특화된 서버입니다.
주요 역할은 다음과 같습니다.

- IMAP/POP3로 메일 클라이언트 접근 제공
- 인증 백엔드(passdb/userdb) 제공
- LMTP로 로컬 메일 전달 수신
- 메일 저장소(Maildir/mbox) 접근 제어

즉, "메일을 실제 사용자에게 보여주고 인증을 담당하는 접근 계층"입니다.

### 2.2 핵심 기능

- IMAP/POP3 서버
- SASL 인증 소켓 제공(Postfix 인증 위임)
- LMTP 수신(로컬 전달)
- 다양한 passdb/userdb 백엔드 지원
- 인덱스/캐시 최적화

### 2.3 실무에서 중요한 설정 항목

- `protocols`
- `mail_location`
- `auth_mechanisms`
- `passdb`, `userdb`
- `service auth`의 unix listener
- `service lmtp`의 unix listener
- `ssl`, `ssl_cert`, `ssl_key`

### 2.4 주의해야 할 점

1. UID/GID 불일치
- 컨테이너/호스트 간 UID/GID 불일치 시 메일 파일 권한 오류가 발생합니다.
- Postfix와 Dovecot이 같은 메일 저장소 권한 모델을 공유하도록 맞추세요.

2. auth 소켓 권한 문제
- Postfix가 Dovecot auth 소켓에 접근하지 못하면 SMTP AUTH가 실패합니다.
- `service auth` unix listener의 `user/group/mode`를 반드시 검증하세요.

3. TLS 미적용 운영
- 테스트 환경(`ssl = no`) 구성을 운영에 가져가면 계정 정보 노출 위험이 큽니다.
- 운영은 `ssl = required` 권장입니다.

4. Maildir 경로 설계
- `mail_location` 경로 정책이 잘못되면 사용자 분리/백업/복구가 어려워집니다.
- 도메인/유저 기반 경로(`%d/%n`)는 멀티도메인 운영에 유리합니다.

5. POP3/IMAP 동시 정책
- POP3 삭제 정책과 IMAP 동기화 정책이 충돌하면 사용자 혼선이 생깁니다.
- 클라이언트 정책(서버에 복사본 유지 여부)을 운영 문서로 안내하세요.

### 2.5 자주 쓰는 점검 명령

```bash
doveconf -n
dovecot -n
# 로그 확인
journalctl -u dovecot -n 200 --no-pager
```

---

## 3) systemd

### 3.1 제품 개요

systemd는 현대 리눅스 배포판의 표준 init/service 관리자입니다.
주요 역할은 다음과 같습니다.

- 서비스 시작/중지/재시작
- 부팅 시 자동 시작 관리
- 장애 자동 복구(Restart 정책)
- 리소스/보안 격리 설정
- 로그 연동(journald)

즉, "프로세스를 운영 관점으로 안정적으로 관리하는 제어 계층"입니다.

### 3.2 핵심 기능

- Unit 파일 기반 선언형 서비스 관리
- 의존성/순서 제어(`After`, `Wants`, `Requires`)
- 재시작 정책(`Restart`, `RestartSec`)
- 권한/보안 하드닝 옵션
- 환경 변수 파일 로딩(`EnvironmentFile`)
- 로그 통합(`journalctl`)

### 3.3 메일 서버 운영에서 중요한 Unit 옵션

- `User`, `Group`
- `WorkingDirectory`
- `EnvironmentFile`
- `ExecStart`, `ExecReload`
- `Restart`, `TimeoutStopSec`
- `NoNewPrivileges`
- `ProtectSystem`, `ProtectHome`
- `ReadWritePaths`
- `CapabilityBoundingSet`

### 3.4 주의해야 할 점

1. 경로 권한과 하드닝 충돌
- `ProtectSystem=strict`를 켜면 기본적으로 쓰기 불가 경로가 많아집니다.
- 반드시 `ReadWritePaths`에 필요한 디렉터리를 명시하세요.

2. 환경 변수 파일 권한
- `EnvironmentFile`에 시크릿이 들어가므로 파일 권한을 제한해야 합니다.
- 권장: `chmod 600` + 전용 서비스 계정 소유.

3. 재시작 루프
- 설정 오류 상태에서 `Restart=always`는 무한 루프를 유발할 수 있습니다.
- `on-failure`와 적절한 `RestartSec` 조합을 권장합니다.

4. reload 의미 혼동
- `ExecReload`는 설정 재적용이 가능한 서비스에서만 의미가 있습니다.
- 애플리케이션이 HUP 처리 미구현이면 reload가 실질적으로 무효일 수 있습니다.

5. 로그 보존/분석
- 장애 분석을 위해 journald 보존 정책을 운영에 맞게 설정하세요.
- 서비스별 `SyslogIdentifier`를 지정하면 필터링이 쉬워집니다.

### 3.5 자주 쓰는 점검 명령

```bash
systemctl daemon-reload
systemctl status mail-admin
systemctl restart mail-admin
systemctl enable mail-admin
journalctl -u mail-admin -n 200 --no-pager
```

---

## 4) ICAP (Internet Content Adaptation Protocol)

### 4.1 제품/프로토콜 개요

ICAP은 HTTP/메일 본문 같은 콘텐츠를 외부 보안 엔진에 넘겨 검사/변환하는 표준 프로토콜입니다.
메일 보안에서는 보통 첨부 파일/본문을 백신 게이트웨이에 전달해 악성코드 여부를 판단할 때 사용합니다.

- 기본 포트 관례: 1344
- 요청 메서드: `REQMOD`, `RESPMOD`, `OPTIONS`
- 동작 위치: SMTP 수신 경로 중 "정책 검사 구간"

쉽게 말해, "메일 본문을 보안 전용 엔진에 위탁 검사하는 연결 표준"입니다.

### 4.2 메일 시스템에서의 역할

일반적인 흐름은 다음과 같습니다.

1. Postfix가 메일을 수신
2. 정책 엔진(Rspamd 또는 별도 필터)이 검사 필요 콘텐츠를 식별
3. ICAP 서버로 검사 요청 전송
4. ICAP 응답(정상/감염/오류)에 따라 허용/거부/격리 결정

즉, ICAP은 Postfix 자체 기능이라기보다 "보안 엔진과 검사 백엔드 간 연계 계층"에 가깝습니다.

### 4.3 핵심 개념

- `OPTIONS`
	- 서버 capability 협상(지원 메서드, Preview 가능 여부 등)
- `REQMOD`
	- 요청 메시지 기준 변환/검사
- `RESPMOD`
	- 응답 메시지 기준 변환/검사
- Preview
	- 본문 일부만 먼저 보내 빠르게 판정 후 필요 시 본문 전체 전송
- 204 응답
	- 변경/조치 없음(No Content), 일반적으로 "통과" 의미

### 4.4 운영 시 주의해야 할 점

1. Fail-open / Fail-close 정책
- 검사 서버 장애 시 메일을 통과시킬지(가용성 우선) 차단할지(보안 우선) 결정이 필요합니다.
- 서비스 성격(금융/공공/일반 SaaS)에 따라 정책이 달라져야 합니다.

2. 타임아웃 및 재시도
- ICAP 타임아웃이 길면 SMTP 처리 지연으로 연결 수가 급증할 수 있습니다.
- 타임아웃, 재시도 횟수, 동시 연결 수 상한을 함께 조정하세요.

3. 대용량 첨부 처리
- 대형 첨부 파일이 많은 환경은 검사 지연과 리소스 폭증 위험이 큽니다.
- 메시지 크기 상한, Preview 전략, 큐 처리량 모니터링을 함께 설계하세요.

4. 탐지 결과 매핑 일관성
- ICAP 결과(감염/의심/오류)를 최종 SMTP 액션(accept/reject/tempfail)으로 일관되게 매핑해야 합니다.
- 운영 문서에 "탐지 시 사용자 체감 결과"를 명확히 정의하세요.

5. 감사/추적성
- 어떤 메시지가 왜 차단되었는지 추적할 수 있어야 민원/장애 대응이 가능합니다.
- 메시지 ID, 검사 엔진 결과, 정책 결정 로그를 상호 연계하세요.

### 4.5 이 저장소에서의 ICAP 포인트

- `ANTIVIRUS_PROVIDER=v3` 설정 시 외부 ICAP 게이트웨이 경로를 사용
- `ANTIVIRUS_V3_ICAP_SERVER`, `ANTIVIRUS_V3_ICAP_SCHEME` 환경 변수로 대상/방식 지정
- 로컬 검증용 `mock-icap` 서비스(프로파일: `v3mock`) 제공

---

## 5) Rspamd

### 5.1 제품 개요

Rspamd는 고성능 메일 필터링 엔진입니다.
스팸 판별뿐 아니라 바이러스 백엔드(ClamAV), 정책 규칙, 평판, 헤더 분석 등을 종합해 최종 액션을 결정합니다.

이 프로젝트에서는 Postfix milter로 연결되어 수신 메일 정책 판단의 핵심 역할을 담당합니다.

### 5.2 핵심 기능

- milter 인터페이스 제공(Postfix 연동)
- 규칙 기반 점수 시스템(symbol/metric)
- ClamAV 연동(첨부 포함 검사)
- DKIM/SPF/DMARC 관련 신호 활용
- Redis/통계 기반 학습(구성에 따라)
- 정책별 액션(reject/add header/no action) 제어

### 5.3 처리 흐름(개념)

1. Postfix가 메시지를 받아 milter로 Rspamd에 전달
2. Rspamd가 헤더/본문/첨부/평판/정책 규칙을 평가
3. 필요 시 ClamAV/ICAP 등 외부 검사 백엔드 호출
4. 점수와 룰 매칭 결과로 최종 액션 산출
5. Postfix가 해당 액션을 반영해 수락/거부/태깅 수행

### 5.4 실무에서 중요한 설정 포인트

- milter 리스닝 소켓/포트
- antivirus 모듈의 백엔드 주소/타임아웃/재시도
- 탐지 시 액션(`reject`, `add header`, `soft reject` 등)
- clean 로그 정책(로그량/비용 관리)
- worker-proxy 타임아웃 및 동시성

### 5.5 장애/성능 측면 주의사항

1. 백엔드 종속 장애
- ClamAV/ICAP 백엔드가 느리거나 죽으면 Rspamd 응답 지연이 바로 SMTP 지연으로 전파됩니다.
- milter 타임아웃과 Postfix 정책을 함께 조율하세요.

2. 과도한 reject 정책
- 오탐(false positive) 시 업무 메일이 손실될 수 있습니다.
- 초기에는 보수적으로 운영하고 로그/샘플 기반 튜닝을 권장합니다.

3. 리소스 한계
- 대량 첨부 검사 시 CPU/메모리 사용량이 급증할 수 있습니다.
- 컨테이너 리소스 제한과 큐 처리량, 평균 검사 시간을 함께 관찰하세요.

4. 로그 신호 해석
- "검사 실패"와 "탐지 성공"을 구분해 해석해야 합니다.
- 실패율 증가(백엔드 문제)와 탐지율 증가(실제 공격)를 별도 지표로 봐야 합니다.

5. 버전/룰 업데이트
- 최신 이미지 태그만 추적하면 예기치 않은 규칙 변화로 정책이 흔들릴 수 있습니다.
- 운영 환경은 버전 고정 + 단계적 업데이트가 안전합니다.

### 5.6 이 저장소에서의 Rspamd 포인트

- Postfix 설정: `smtpd_milters = inet:rspamd:11332`
- Rspamd 로컬 설정 파일:
	- `deploy/docker/rspamd/local.d/worker-proxy.inc`
	- `deploy/docker/rspamd/local.d/antivirus.conf`
- ClamAV 연동 주소: `clamav:3310`

---

## 6) Postfix + Dovecot + ICAP + Rspamd + systemd 함께 볼 때 체크리스트

1. 인증 체인
- Postfix SMTP AUTH -> Dovecot auth 소켓 연결 정상 여부

2. 전달 체인
- Postfix `virtual_transport` -> Dovecot LMTP 소켓 연결 정상 여부

3. 저장소 권한
- `/var/mail/...` 경로의 UID/GID 일관성

4. 보안 체인
- Postfix milter -> Rspamd -> ClamAV/ICAP 연동 및 장애 시 정책 확인

5. ICAP 정책 일관성
- 감염/오류/타임아웃 응답이 최종 SMTP 액션으로 의도대로 매핑되는지 확인

6. 서비스 자동복구
- systemd 재시작 정책과 헬스체크/알림 체계 구성

7. 운영 로그
- Postfix, Dovecot, 애플리케이션 로그를 시간축으로 같이 보는 습관

---

## 7) 이 저장소 기준 참고 파일

- Postfix 컨테이너 설정: `deploy/docker/postfix/main.cf`
- Dovecot 컨테이너 설정: `deploy/docker/dovecot/dovecot.conf`
- Rspamd 로컬 설정: `deploy/docker/rspamd/local.d/antivirus.conf`
- Rspamd 워커 설정: `deploy/docker/rspamd/local.d/worker-proxy.inc`
- systemd 유닛 예시: `deploy/systemd/mail-admin.service`
- ICAP mock 서버: `deploy/docker/mock-icap/server.py`
- Docker Compose 통합 구성: `docker-compose.yml`

이 파일들을 같이 보면 실제 연결 구조를 가장 빠르게 이해할 수 있습니다.

---

## 8) 환경설정 항목 상세 레퍼런스

이 섹션은 운영자가 "어떤 값을 왜 바꾸는지" 빠르게 판단할 수 있도록,
제품별 핵심 설정 항목을 목적/권장값/주의점 기준으로 정리한 참고표입니다.

### 8.1 Postfix 항목 레퍼런스

| 항목 | 설명 | 권장/운영 포인트 | 잘못 설정 시 증상 |
|---|---|---|---|
| `myhostname` | SMTP 서버 식별 호스트명 | DNS A/PTR, TLS 인증서 이름과 일치 권장 | 상대 MTA 신뢰도 저하, 스팸 점수 증가 |
| `mydomain` | 기본 메일 도메인 | 실제 수신 도메인과 동일하게 유지 | 도메인 불일치 반송 증가 |
| `myorigin` | 로컬 발신 기본 도메인 | 보통 `$mydomain` 사용 | 발신 주소 정합성 저하 |
| `inet_interfaces` | 리슨 인터페이스 | 운영망 분리 시 특정 인터페이스 제한 | 외부/내부 접속 불가 |
| `mynetworks` | 릴레이 허용 네트워크 | 최소 범위 원칙 적용 | 오픈 릴레이 위험 |
| `smtpd_recipient_restrictions` | 수신 정책 체인 | `reject_unauth_destination` 필수 포함 | 무단 릴레이 또는 정상 메일 차단 |
| `virtual_mailbox_domains` | 가상 도메인 맵 | 생성 파일 + `postmap` 갱신 자동화 | 도메인 미인식 |
| `virtual_mailbox_maps` | 사용자 메일박스 맵 | 사용자 변경 시 즉시 동기화 | 수신자 없음 오류 |
| `virtual_transport` | 최종 전달 경로 | Dovecot LMTP 소켓과 정확히 일치 | 수신 후 저장 실패 |
| `smtpd_sasl_auth_enable` | SMTP AUTH 사용 여부 | submission(587)에서 활성화 권장 | 클라이언트 인증 실패 |
| `smtpd_sasl_type`, `smtpd_sasl_path` | 인증 백엔드/소켓 | dovecot + `private/auth` 경로 일치 | 로그인 535 오류 |
| `smtpd_tls_security_level` | 수신 TLS 정책 | 운영은 인증서 + stricter 정책 권장 | 평문 노출/핸드셰이크 실패 |
| `smtpd_milters`, `non_smtpd_milters` | 보안 필터 연동 | Rspamd 주소, 장애 정책 함께 설계 | 메일 지연/필터 우회 |
| `milter_default_action` | milter 실패 시 기본 동작 | 가용성 우선이면 accept, 보안 우선이면 tempfail/reject | 장애 전파 정책 불일치 |

### 8.2 Dovecot 항목 레퍼런스

| 항목 | 설명 | 권장/운영 포인트 | 잘못 설정 시 증상 |
|---|---|---|---|
| `protocols` | 활성 프로토콜(IMAP/POP3/LMTP) | 필요한 프로토콜만 최소 활성화 | 포트는 열렸지만 서비스 불능 |
| `mail_location` | 메일 저장 경로 템플릿 | 멀티도메인은 `%d/%n` 구조 권장 | 메일함 비어 보임/경로 충돌 |
| `auth_mechanisms` | 인증 방식 | TLS 전제에서 `plain login` 사용 | 인증 실패 또는 보안 취약 |
| `passdb` | 비밀번호 조회 소스 | passwd-file/sql/ldap 중 정책 일관 유지 | 계정 존재해도 인증 실패 |
| `userdb` | UID/GID/home 매핑 | Postfix와 동일 권한 모델 유지 | 파일 권한 오류 |
| `service auth` listener | Postfix AUTH 소켓 | 경로/권한(mode,user,group) 정합성 확인 | SMTP AUTH 실패 |
| `service lmtp` listener | Postfix 전달 소켓 | `virtual_transport`와 1:1 대응 | 메일 수신 후 저장 실패 |
| `ssl`, `ssl_cert`, `ssl_key` | TLS 강제/인증서 | 운영은 `ssl=required` 권장 | 평문 인증 노출, TLS 실패 |
| `first_valid_uid`, `last_valid_uid` | 허용 UID 범위 | 컨테이너/호스트 UID 전략과 통일 | 접근 거부/권한 오류 |

### 8.3 Rspamd 항목 레퍼런스

| 항목 | 설명 | 권장/운영 포인트 | 잘못 설정 시 증상 |
|---|---|---|---|
| `bind_socket` | milter 리슨 주소 | Postfix `smtpd_milters` 대상과 일치 | 필터 미적용/연결 실패 |
| `milter` | milter 모드 활성화 | Postfix 연동 시 반드시 yes | 정책 평가 미실행 |
| `timeout` (worker) | milter 응답 제한시간 | SMTP 처리 지연 상한 고려 | 지연 급증 또는 오탐성 실패 |
| `type` (antivirus) | 백엔드 유형 | `clamav` 사용 시 명확히 지정 | 바이러스 모듈 비활성 |
| `servers` | 백엔드 주소 목록 | DNS/네트워크 안정성 보장 | 검사 실패율 상승 |
| `scan_mime_parts` | MIME 파트 검사 | 첨부 탐지 정확도 향상 | 첨부 악성코드 누락 |
| `action` | 탐지 시 조치 | 초기엔 보수적 운영 후 점진 강화 | 오탐 시 메일 손실 |
| `retransmits` | 재시도 횟수 | 지연과 성공률 균형 조정 | 장애 시 응답 지연 확대 |
| `timeout` (antivirus) | 백엔드 검사 타임아웃 | 평균 첨부 크기 기준으로 조정 | tempfail/reject 급증 |
| `log_clean` | 정상 메일 로그 기록 | 비용/추적성 균형으로 선택 | 로그 과다 또는 가시성 부족 |

### 8.4 ICAP 연동 항목 레퍼런스

이 저장소는 ICAP 자체 서버 설정 파일보다 애플리케이션 환경변수 기반 연동 제어를 사용합니다.

| 항목 | 설명 | 권장/운영 포인트 | 잘못 설정 시 증상 |
|---|---|---|---|
| `ANTIVIRUS_PROVIDER` | 백신 경로 선택 | `clamav`/`v3`/`none` 중 운영정책과 일치 | 의도와 다른 검사 체인 사용 |
| `ANTIVIRUS_FAIL_OPEN` | 검사 실패 시 허용 여부 | 가용성 우선 yes, 보안 우선 no | 장애 시 메일 유실/우회 |
| `ANTIVIRUS_V3_ICAP_SERVER` | ICAP 서버 주소 | `host:1344` 형태, 이중화 고려 | 연결 실패/지연 증가 |
| `ANTIVIRUS_V3_ICAP_SCHEME` | 요청 스킴 | `respmod`/`reqmod`를 정책과 일치 | 검사 범위 불일치 |

추가 운영 팁:

- ICAP 타임아웃은 SMTP 타임아웃보다 과도하게 길지 않게 유지합니다.
- 장애 시 정책(accept/tempfail/reject)을 사용자 공지 정책과 함께 문서화합니다.
- 감염 탐지, 검사 실패, 타임아웃 지표를 분리 모니터링합니다.

### 8.5 systemd Unit 항목 레퍼런스

| 항목 | 설명 | 권장/운영 포인트 | 잘못 설정 시 증상 |
|---|---|---|---|
| `User`, `Group` | 실행 계정 | 비루트 전용 계정 권장 | 권한 과다/보안 위험 |
| `WorkingDirectory` | 작업 디렉터리 | 상대 경로/출력 파일 기준점 | 파일 탐색 실패 |
| `EnvironmentFile` | 환경 변수 파일 로드 | 권한 600 + 소유자 제한 | 시크릿 노출/기동 실패 |
| `ExecStart` | 시작 명령 | 바이너리/인자 절대경로 사용 | 즉시 종료 |
| `ExecReload` | 재적용 명령 | 앱 HUP 처리 지원 여부 확인 | reload 무효 |
| `Restart`, `RestartSec` | 자동 복구 정책 | `on-failure` + 적절한 간격 | 재시작 폭주 |
| `TimeoutStopSec` | 종료 대기 시간 | graceful shutdown 시간 반영 | 강제 종료/배포 지연 |
| `NoNewPrivileges` | 권한 상승 차단 | 기본 활성화 권장 | 특수 기능 사용 제한 |
| `ProtectSystem`, `ProtectHome` | 파일시스템 보호 | 필요한 쓰기 경로는 별도 허용 | 런타임 쓰기 실패 |
| `ReadWritePaths` | 쓰기 허용 경로 | data/generated 등 명시 | DB/생성 파일 오류 |
| `CapabilityBoundingSet` | Linux capability 제한 | 가능한 최소권한 유지 | 필요 capability 누락 시 실패 |
| `SyslogIdentifier` | journald 식별자 | 다중 서비스 로그 분리 용이 | 로그 추적 난이도 증가 |

### 8.6 운영 우선 점검 Top 10

1. Postfix `smtpd_recipient_restrictions`에 `reject_unauth_destination` 포함 여부
2. Postfix `virtual_mailbox_maps` 갱신 + `postmap` 적용 여부
3. Postfix `virtual_transport`와 Dovecot LMTP 소켓 일치 여부
4. Postfix `smtpd_milters` 주소와 Rspamd `bind_socket` 정합성
5. Dovecot `service auth` 소켓 권한(user/group/mode)
6. Dovecot `mail_location` 경로와 UID/GID 권한 모델 정합성
7. Rspamd antivirus `servers` 가용성 및 `timeout` 적정성
8. ICAP 관련 환경변수(`ANTIVIRUS_PROVIDER`, `ANTIVIRUS_FAIL_OPEN`) 정책 일관성
9. systemd `ReadWritePaths`에 실제 쓰기 경로 포함 여부
10. 장애 시 로그 상관분석 체계(Postfix/Dovecot/Rspamd/App) 준비 여부
