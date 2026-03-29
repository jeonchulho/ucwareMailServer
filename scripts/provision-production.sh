#!/usr/bin/env bash
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "Run as root"
  exit 1
fi

if [[ $# -lt 4 ]]; then
  echo "Usage: $0 <domain> <mail-hostname> <admin-email> <server-ip>"
  echo "Example: $0 example.com mail.example.com admin@example.com 203.0.113.10"
  exit 1
fi

domain="$1"
mail_host="$2"
admin_email="$3"
server_ip="$4"
dkim_selector="default"

# Antivirus provider: clamav | v3 | none
antivirus_provider="${ANTIVIRUS_PROVIDER:-clamav}"
v3_icap_server="${ANTIVIRUS_V3_ICAP_SERVER:-}"
v3_icap_scheme="${ANTIVIRUS_V3_ICAP_SCHEME:-respmod}"
antivirus_fail_open="${ANTIVIRUS_FAIL_OPEN:-yes}"

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing command: $1"
    exit 1
  fi
}

echo "[1/8] Installing packages"
apt-get update

base_packages=(
  postfix dovecot-core dovecot-imapd dovecot-lmtpd
  rspamd opendkim opendkim-tools certbot dnsutils
)

case "$antivirus_provider" in
  clamav)
    base_packages+=(clamav clamav-daemon)
    ;;
  v3|none)
    ;;
  *)
    echo "Unsupported ANTIVIRUS_PROVIDER: $antivirus_provider (allowed: clamav|v3|none)"
    exit 1
    ;;
esac

apt-get install -y --no-install-recommends "${base_packages[@]}"

need_cmd certbot
need_cmd opendkim-genkey

echo "[2/8] Issuing TLS certificate with certbot"
certbot certonly --standalone \
  -d "$mail_host" \
  --non-interactive \
  --agree-tos \
  -m "$admin_email" \
  --keep-until-expiring

echo "[3/8] Preparing OpenDKIM directories"
mkdir -p "/etc/opendkim/keys/$domain"
chown -R opendkim:opendkim /etc/opendkim

if [[ ! -f "/etc/opendkim/keys/$domain/$dkim_selector.private" ]]; then
  echo "[4/8] Generating DKIM key"
  opendkim-genkey -D "/etc/opendkim/keys/$domain" -d "$domain" -s "$dkim_selector"
  chown opendkim:opendkim "/etc/opendkim/keys/$domain/$dkim_selector.private"
fi

echo "[5/8] Writing OpenDKIM config files"
cat > /etc/opendkim.conf <<EOF
Syslog                  yes
Canonicalization        relaxed/simple
Mode                    sv
SubDomains              no
OversignHeaders         From
KeyTable                /etc/opendkim/key.table
SigningTable            refile:/etc/opendkim/signing.table
ExternalIgnoreList      /etc/opendkim/trusted.hosts
InternalHosts           /etc/opendkim/trusted.hosts
Socket                  local:/var/spool/postfix/opendkim/opendkim.sock
EOF

cat > /etc/opendkim/key.table <<EOF
$dkim_selector._domainkey.$domain $domain:$dkim_selector:/etc/opendkim/keys/$domain/$dkim_selector.private
EOF

cat > /etc/opendkim/signing.table <<EOF
*@$domain $dkim_selector._domainkey.$domain
EOF

cat > /etc/opendkim/trusted.hosts <<EOF
127.0.0.1
localhost
$mail_host
$server_ip
EOF

mkdir -p /var/spool/postfix/opendkim
chown opendkim:postfix /var/spool/postfix/opendkim

echo "[6/8] Wiring OpenDKIM + Rspamd into Postfix"
postconf -e "milter_default_action = accept"
postconf -e "milter_protocol = 6"
postconf -e "smtpd_milters = inet:127.0.0.1:11332,unix:/opendkim/opendkim.sock"
postconf -e "non_smtpd_milters = inet:127.0.0.1:11332,unix:/opendkim/opendkim.sock"

mkdir -p /etc/rspamd/local.d

echo "[6.1/8] Configuring antivirus provider: $antivirus_provider"
case "$antivirus_provider" in
  clamav)
    if ! grep -q '^TCPSocket 3310' /etc/clamav/clamd.conf; then
      echo 'TCPSocket 3310' >> /etc/clamav/clamd.conf
    fi
    if ! grep -q '^TCPAddr 127.0.0.1' /etc/clamav/clamd.conf; then
      echo 'TCPAddr 127.0.0.1' >> /etc/clamav/clamd.conf
    fi

    cat > /etc/rspamd/local.d/antivirus.conf <<EOF
clamav {
  type = "clamav";
  servers = "127.0.0.1:3310";
  scan_mime_parts = true;
  action = "reject";
  log_clean = false;
  retransmits = 2;
  timeout = 20s;
}
EOF
    ;;
  v3)
    if [[ -z "$v3_icap_server" ]]; then
      echo "ANTIVIRUS_V3_ICAP_SERVER is required when ANTIVIRUS_PROVIDER=v3"
      echo "example: export ANTIVIRUS_V3_ICAP_SERVER=10.0.0.5:1344"
      exit 1
    fi

    cat > /etc/rspamd/local.d/antivirus.conf <<EOF
v3_icap {
  type = "icap";
  servers = "$v3_icap_server";
  scheme = "$v3_icap_scheme";
  scan_mime_parts = true;
  action = "reject";
  log_clean = false;
  retransmits = 2;
  timeout = 20s;
}
EOF
    ;;
  none)
    cat > /etc/rspamd/local.d/antivirus.conf <<EOF
# Antivirus is disabled by ANTIVIRUS_PROVIDER=none
EOF
    ;;
esac

if [[ "$antivirus_fail_open" == "yes" ]]; then
  postconf -e "milter_default_action = accept"
else
  postconf -e "milter_default_action = tempfail"
fi

echo "[7/8] Ensuring service startup"
if [[ "$antivirus_provider" == "clamav" ]]; then
  systemctl enable clamav-daemon
  systemctl restart clamav-daemon
fi

systemctl enable opendkim postfix dovecot rspamd
systemctl restart opendkim
systemctl restart postfix
systemctl restart dovecot
systemctl restart rspamd

echo "[8/8] Writing DNS records guide"
mkdir -p /root/mail-dns
cp "/etc/opendkim/keys/$domain/$dkim_selector.txt" "/root/mail-dns/dkim-${domain}.txt"
cat > "/root/mail-dns/dns-${domain}.txt" <<EOF
Add the following DNS records:

1) MX
   $domain. IN MX 10 $mail_host.

2) A
   $mail_host. IN A $server_ip

3) SPF
   $domain. IN TXT "v=spf1 mx -all"

4) DMARC
   _dmarc.$domain. IN TXT "v=DMARC1; p=quarantine; adkim=s; aspf=s; rua=mailto:dmarc@$domain"

5) DKIM
   See /root/mail-dns/dkim-${domain}.txt

6) PTR
   Ask your cloud provider to map $server_ip -> $mail_host
EOF

echo "Done. DNS guidance is in /root/mail-dns/dns-${domain}.txt"
