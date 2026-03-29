#!/usr/bin/env bash
# scripts/deploy.sh
# 새 바이너리를 운영 서버에 배포합니다.
# 사용법: sudo ./scripts/deploy.sh --binary ./mail-admin [--install-dir /opt/mail-admin]
set -euo pipefail

BINARY_SRC=""
INSTALL_DIR="/opt/mail-admin"
SERVICE_NAME="mail-admin"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary)       BINARY_SRC="$2"; shift 2 ;;
    --install-dir)  INSTALL_DIR="$2"; shift 2 ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

if [[ $EUID -ne 0 ]]; then
  echo "[ERROR] root 권한으로 실행해야 합니다." >&2; exit 1
fi
if [[ -z "$BINARY_SRC" || ! -f "$BINARY_SRC" ]]; then
  echo "[ERROR] --binary <path> 를 지정하세요." >&2; exit 1
fi

BINARY_DEST="${INSTALL_DIR}/mail-admin"
BACKUP="${BINARY_DEST}.prev"

echo "[INFO] ${INSTALL_DIR} 디렉터리 준비…"
mkdir -p "${INSTALL_DIR}/data" "${INSTALL_DIR}/generated"

# 기존 바이너리 백업
if [[ -f "${BINARY_DEST}" ]]; then
  echo "[INFO] 기존 바이너리 백업: ${BINARY_DEST} → ${BACKUP}"
  cp "${BINARY_DEST}" "${BACKUP}"
fi

echo "[INFO] 새 바이너리 배포…"
cp "${BINARY_SRC}" "${BINARY_DEST}.new"
chmod 755 "${BINARY_DEST}.new"
mv "${BINARY_DEST}.new" "${BINARY_DEST}"

echo "[INFO] 서비스 재시작…"
systemctl restart "${SERVICE_NAME}"

echo "[INFO] 헬스체크 대기…"
for i in $(seq 1 15); do
  if curl -sf http://127.0.0.1:8080/healthz > /dev/null 2>&1; then
    echo "[OK] 서비스 정상 기동 (시도 ${i})"
    break
  fi
  if [[ $i -eq 15 ]]; then
    echo "[ERROR] 서비스 응답 없음 — 자동 롤백 실행" >&2
    "$(dirname "$0")/rollback.sh" --install-dir "${INSTALL_DIR}" || true
    exit 1
  fi
  sleep 2
done

echo "✓ 배포 완료"
