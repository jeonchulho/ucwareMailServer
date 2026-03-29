# Postfix master.cf 해석 및 작성 가이드 (실무 상세)

이 문서는 현재 저장소의 Postfix 서비스 정의 파일인 deploy/docker/postfix/master.cf를 기준으로,
다음 두 가지를 매우 자세히 설명합니다.

1. 현재 파일이 무엇을 의미하는지 (줄 단위 해석)
2. 새로 작성/수정할 때 어떤 규칙으로 작성해야 안전한지

---

## 1. master.cf의 역할

master.cf는 Postfix의 서비스 프로세스 표(service table)입니다.

쉽게 말해 다음을 결정합니다.

- 어떤 서비스를 열 것인지
- 네트워크 서비스인지(inet), 내부 유닉스 소켓 서비스인지(unix)
- 권한/격리/프로세스 상한을 어떻게 둘 것인지
- 어떤 데몬(command)으로 실행할 것인지

중요:

- main.cf는 정책의 기본값(what)
- master.cf는 서비스 실행 구조(how)

두 파일은 서로 보완 관계입니다.

---

## 2. 한 줄의 기본 문법

master.cf 기본 행은 아래 8개 필드로 구성됩니다.

- service
- type
- private
- unpriv
- chroot
- wakeup
- maxproc
- command + args

형식 예시:

service  type  private  unpriv  chroot  wakeup  maxproc  command

필드 의미:

1. service
- 서비스 이름
- 예: smtp, submission, pickup, qmgr

2. type
- inet: TCP 포트로 외부 요청 수신
- unix: Postfix 내부 프로세스 통신용 소켓

3. private
- y면 private 서비스 네임스페이스에서 사용
- n이면 public 성격
- 기본 내장 서비스는 배포판 기본값을 따르는 것이 안전

4. unpriv
- y/n 또는 -
- 비권한 실행 여부

5. chroot
- y/n 또는 -
- chroot 격리 여부

6. wakeup
- 주기적으로 깨울 간격
- - 는 주기 깨우기 없음

7. maxproc
- 동시 프로세스 상한
- - 는 기본값

8. command + args
- 실제 데몬
- 예: smtpd, cleanup, qmgr, bounce

---

## 3. 현재 파일 줄 단위 해석

대상 파일:

- deploy/docker/postfix/master.cf

### 3.1 외부 수신/발송 엔트리

1) smtp inet ... smtpd
- 25번 SMTP 인바운드 수신용 엔트리
- 외부 MTA가 메일을 넣을 때 주로 사용

2) submission inet ... smtpd
- 587번 submission 엔트리
- 클라이언트 인증 기반 발송 경로

3) submission 아래 -o 오버라이드
- -o syslog_name=postfix/submission
  - 로그 식별자를 submission 전용으로 분리
- -o smtpd_tls_security_level=may
  - TLS 사용 가능(강제 아님)
- -o smtpd_sasl_auth_enable=yes
  - SMTP AUTH 활성화

핵심 포인트:

- 같은 smtpd라도 service 단위로 옵션을 분리해 정책을 다르게 줄 수 있음
- 이것이 master.cf의 가장 큰 장점

### 3.2 내부 큐/처리 파이프라인 엔트리

아래는 외부 포트 서비스가 아니라 Postfix 내부 동작을 위한 서비스입니다.

- pickup
  - 메일 제출 큐에서 메시지를 주워 처리 파이프라인에 넣음

- cleanup
  - 헤더/큐 파일 정리, 표준화 작업

- qmgr
  - 큐 매니저, 메시지 전달 스케줄링 핵심

- tlsmgr
  - TLS 관련 상태/세션 관리

- rewrite (trivial-rewrite)
  - 주소 재작성/라우팅 관련 기본 처리

- bounce, defer, trace, verify
  - 반송/지연/추적/검증 관련 유틸리티 서비스

- flush
  - 큐 flush 타이밍 관련 서비스

- proxymap, proxywrite
  - 매핑 조회/기록 보조 서비스

- smtp, relay
  - 외부 전달 SMTP 클라이언트 역할

- showq
  - 큐 조회 보조

- error, retry, discard
  - 에러 핸들링/재시도/폐기 경로

- local, virtual
  - 로컬/가상 도메인 전달 경로

- lmtp
  - LMTP 클라이언트
  - 현재 아키텍처에서는 Dovecot LMTP로 전달할 때 중요

- anvil
  - 연결/요청 속도 관련 카운팅(안전장치 성격)

- scache
  - 세션 캐시 관련 보조 서비스

---

## 4. 작성 방식 (실무 규칙)

### 4.1 작성 기본 원칙

1. main.cf는 기본 정책, master.cf는 서비스별 예외만
- 공통 정책을 master.cf에 과하게 넣지 말고 main.cf에 둔다

2. 수정은 최소 단위로
- submission 블록처럼 필요한 서비스에만 -o 오버라이드 추가

3. 내부 unix 서비스는 의미를 알고 수정
- pickup/qmgr/cleanup 등은 메일 생명주기 핵심
- 모르면 기본값 유지가 안전

4. 변경 전후 비교를 반드시 남김
- 어떤 서비스에 어떤 옵션을 왜 바꿨는지 주석과 변경 기록 남기기

### 4.2 주석 작성 패턴 권장

권장 패턴:

- 블록 위 주석: 서비스 목적
- 오버라이드 위 주석: 기본값 대비 무엇을 왜 덮어쓰는지

예시(형식):

# Submission(587) 인증 발송 서비스
submission inet n - y - - smtpd
  # 로그 분리
  -o syslog_name=postfix/submission
  # 인증 허용
  -o smtpd_sasl_auth_enable=yes

### 4.3 자주 하는 실수

1. smtp(25)에 client submission 정책을 그대로 적용
- 외부 수신과 사용자 발송은 분리해야 함

2. -o 옵션을 잘못된 블록에 붙임
- 들여쓰기 된 -o는 바로 위 서비스에만 적용됨

3. 내부 서비스 maxproc를 과도하게 높임
- 메모리 압박과 컨텍스트 스위칭 비용 증가

4. chroot/unpriv를 의미 없이 바꿈
- 파일 경로/권한 문제로 기동 실패 가능

5. main.cf와 master.cf 역할 혼동
- 정책이 어디서 덮이는지 추적이 어려워짐

---

## 5. 안전한 변경 절차

1. 변경 전 백업
- 현재 master.cf 복사본 저장

2. 최소 변경 적용
- 서비스 1개 단위로 변경

3. 설정 검증

```bash
postfix check
postconf -n
```

4. 서비스 반영

```bash
postfix reload
```

컨테이너 환경이면 해당 컨테이너 재기동/로그 확인

5. 기능 검증
- SMTP 25 수신 테스트
- submission 587 인증 발송 테스트
- LMTP 전달 테스트
- 큐 상태 확인

```bash
postqueue -p
```

---

## 6. 상황별 작성 예시

### 6.1 submission에만 인증 강제하고 싶을 때

- submission 서비스에 -o smtpd_sasl_auth_enable=yes
- 필요 시 -o smtpd_recipient_restrictions 별도 오버라이드

### 6.2 특정 서비스 로그를 분리하고 싶을 때

- -o syslog_name=postfix/<service>
- 예: postfix/submission

### 6.3 성능 튜닝으로 maxproc 조정할 때

- queue 길이, CPU, 메모리, 지연을 함께 보며 단계적으로 증가
- 한 번에 크게 올리지 않는다

---

## 7. 운영 점검 체크리스트

1. smtp(25)와 submission(587) 정책이 분리되어 있는가
2. submission에서 SMTP AUTH가 실제로 동작하는가
3. LMTP 전달 경로가 Dovecot과 일치하는가
4. 큐가 비정상적으로 누적되지 않는가
5. milter/보안 체인 장애가 전파될 때 정책이 의도대로 동작하는가
6. 로그 식별자 분리가 되어 있는가

---

## 8. 이 저장소에서 함께 봐야 하는 파일

- deploy/docker/postfix/main.cf
- deploy/docker/postfix/master.cf
- deploy/docker/postfix/start.sh
- deploy/docker/dovecot/dovecot.conf
- docker-compose.yml
- docker-compose_large.yml

이 파일들을 함께 보면 정책(main.cf)과 실행(master.cf), 전달(dovecot), 기동(start.sh), 배포(compose) 연결을 한 번에 이해할 수 있습니다.
