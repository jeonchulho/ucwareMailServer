#!/usr/bin/env bash
# scripts/rollback.sh
# 이전 바이너리로 롤백합니다.
# 사용법: sudo ./scripts/rollback.sh [--install-dir /opt/mail-admin]
set -euo pipefail

INSTALL_DIR="/opt/mail-admin"
SERVICE_NAME="mail-admin"
BINARY="${INSTALL_DIR}/mail-admin"
BACKUP="${BINARY}.prev"

# ── 인자 파싱 ─────────────────────────────────────────────────────────────────
while [[ $# -gt 0 ]]; do
  case "$1" in
    --install-dir) INSTALL_DIR="$2"; shift 2 ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

BINARY="${INSTALL_DIR}/mail-admin"
BACKUP="${BINARY}.prev"

# ── 사전 확인 ─────────────────────────────────────────────────────────────────
if [[ $EUID -ne 0 ]]; then
  echo "[ERROR] root 권한으로 실행해야 합니다." >&2
  exit 1
fi

if [[ ! -f "${BACKUP}" ]]; then
  echo "[ERROR] 이전 바이너리가 없습니다: ${BACKUP}" >&2
  echo "        deploy.sh 로 배포한 적이 없거나 이미 정리된 상태입니다." >&2
  exit 1
fi

CURRENT_SUM=""
BACKUP_SUM=""
if command -v sha256sum &>/dev/null; then
  CURRENT_SUM=$(sha256sum "${BINARY}" 2>/dev/null | awk '{print $1}' || true)
  BACKUP_SUM=$(sha256sum "${BACKUP}" | awk '{print $1}')
fi

if [[ -n "$CURRENT_SUM" && "$CURRENT_SUM" == "$BACKUP_SUM" ]]; then
  echo "[WARN] 현재 바이너리와 이전 바이너리가 동일합니다. 롤백이 필요하지 않을 수 있습니다."
  read -r -p "그래도 계속하시겠습니까? [y/N] " answer
  [[ "${answer,,}" == "y" ]] || { echo "취소."; exit 0; }
fi

# ── 롤백 실행 ─────────────────────────────────────────────────────────────────
echo "[INFO] 서비스 중지 중…"
systemctl stop "${SERVICE_NAME}"

echo "[INFO] 현재 바이너리를 .bad 로 이동: ${BINARY} → ${BINARY}.bad"
mv "${BINARY}" "${BINARY}.bad"

echo "[INFO] 이전 바이너리로 복원: ${BACKUP} → ${BINARY}"
cp "${BACKUP}" "${BINARY}"
chmod 755 "${BINARY}"

echo "[INFO] 서비스 재시작 중…"
systemctl start "${SERVICE_NAME}"

# ── 헬스체크 ─────────────────────────────────────────────────────────────────
echo "[INFO] 헬스체크 대기 중…"
for i in $(seq 1 10); do
  if curl -sf http://127.0.0.1:8080/healthz > /dev/null 2>&1; then
    echo "[OK] 서비스 정상 기동 확인 (시도 ${i})"
    break
  fi
  if [[ $i -eq 10 ]]; then
    echo "[ERROR] 롤백 후에도 서비스 응답 없음!" >&2
    systemctl status "${SERVICE_NAME}" --no-pager >&2 || true
    exit 1
  fi
  sleep 2
done

echo ""
echo "✓ 롤백 완료"
echo "  복원된 바이너리 : ${BINARY}"
echo "  실패한 바이너리 : ${BINARY}.bad  (수동 삭제 가능)"
