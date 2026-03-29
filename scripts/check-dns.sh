#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "Usage: $0 <domain> <mail-hostname> [server-ip]"
  echo "Example: $0 example.com mail.example.com 203.0.113.10"
  exit 1
fi

domain="$1"
mail_host="$2"
server_ip="${3:-}"

if ! command -v dig >/dev/null 2>&1; then
  echo "dig command not found. Install dnsutils first."
  exit 1
fi

line() {
  printf '%s\n' "----------------------------------------"
}

show_records() {
  local title="$1"
  local query="$2"
  line
  echo "$title"
  dig +short "$query" | sed '/^$/d' || true
}

echo "Mail DNS checker"
echo "domain: $domain"
echo "mail host: $mail_host"

show_records "MX records" "$domain MX"
show_records "A/AAAA records for mail host" "$mail_host A"
dig +short "$mail_host" AAAA | sed '/^$/d' || true

line
echo "SPF record"
dig +short "$domain" TXT | grep -E 'v=spf1' || echo "SPF record not found"

line
echo "DMARC record"
dig +short "_dmarc.$domain" TXT | grep -E 'v=DMARC1' || echo "DMARC record not found"

line
echo "DKIM selector checks (default/mail/google)"
for selector in default mail google; do
  rec="${selector}._domainkey.$domain"
  out="$(dig +short "$rec" TXT | sed '/^$/d' || true)"
  if [[ -n "$out" ]]; then
    echo "found: $rec"
    echo "$out"
  else
    echo "missing: $rec"
  fi
done

if [[ -n "$server_ip" ]]; then
  line
  echo "PTR record"
  dig +short -x "$server_ip" | sed '/^$/d' || echo "PTR record not found"
fi

line
echo "Done"
