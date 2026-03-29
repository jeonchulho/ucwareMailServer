FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install -y --no-install-recommends dovecot-core dovecot-imapd dovecot-lmtpd ca-certificates \
    && rm -rf /var/lib/apt/lists/*

EXPOSE 143 993
ENTRYPOINT ["/usr/sbin/dovecot", "-F"]
