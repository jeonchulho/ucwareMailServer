FROM ubuntu:24.04

# apt 상호작용 프롬프트를 비활성화해 비대화형 빌드를 보장한다.
ENV DEBIAN_FRONTEND=noninteractive

# IMAP/LMTP 동작에 필요한 최소 Dovecot 패키지만 설치한다.
RUN apt-get update \
    && apt-get install -y --no-install-recommends dovecot-core dovecot-imapd dovecot-lmtpd ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# 143: IMAP, 993: IMAPS
EXPOSE 143 993
# 포그라운드 모드로 실행해 컨테이너 PID 1 프로세스로 유지한다.
ENTRYPOINT ["/usr/sbin/dovecot", "-F"]
