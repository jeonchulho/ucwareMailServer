#!/usr/bin/env bash
set -euo pipefail

postmap /etc/postfix/generated/virtual_mailbox_maps || true
postmap /etc/postfix/generated/virtual_mailbox_domains || true

exec /usr/sbin/postfix start-fg
