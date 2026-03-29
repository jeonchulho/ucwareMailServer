#!/usr/bin/env bash
set -euo pipefail

# 생성된 맵 파일이 있으면 postmap DB를 갱신한다.
# 초기 기동 시 파일이 없을 수 있어 실패를 무시한다.
postmap /etc/postfix/generated/virtual_mailbox_maps || true
postmap /etc/postfix/generated/virtual_mailbox_domains || true

# postfix를 포그라운드로 실행해 컨테이너 메인 프로세스로 유지한다.
exec /usr/sbin/postfix start-fg
