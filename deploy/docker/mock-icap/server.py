import socketserver
from datetime import datetime, timezone

# 컨테이너 외부에서 접근 가능하도록 모든 인터페이스에 바인딩한다.
HOST = "0.0.0.0"
# ICAP 표준 포트(여기서는 mock 서버 전용)로 수신한다.
PORT = 1344
# ICAP OPTIONS 응답에서 노출할 서비스 식별자.
SERVICE = "avscan"


def now_http_date() -> str:
    # ICAP/HTTP 헤더에 쓰는 RFC 1123 형태의 UTC 문자열을 생성한다.
    return datetime.now(timezone.utc).strftime("%a, %d %b %Y %H:%M:%S GMT")


class ICAPHandler(socketserver.BaseRequestHandler):
    def handle(self) -> None:
        # 단일 TCP 연결에서 들어온 ICAP 요청 원문을 메모리에 누적한다.
        data = b""
        # 테스트 환경에서 무한 대기하지 않도록 소켓 타임아웃을 짧게 둔다.
        self.request.settimeout(3)
        while True:
            try:
                chunk = self.request.recv(4096)
            except Exception:
                # 타임아웃/소켓 오류가 나면 현재까지 받은 데이터로 처리하고 종료한다.
                break
            if not chunk:
                # 클라이언트가 연결을 닫으면 수신 루프를 끝낸다.
                break
            data += chunk
            # chunked 전송의 종료 마커를 보거나, 비정상 대용량(1MB 초과)일 때 읽기를 멈춘다.
            # mock 서버 특성상 단순/안전하게 상한을 둔다.
            if b"\r\n0\r\n\r\n" in data or len(data) > 1024 * 1024:
                break

        if not data:
            return

        try:
            # 첫 줄(예: OPTIONS icap://... ICAP/1.0)을 파싱해 메서드를 판별한다.
            request_line = data.split(b"\r\n", 1)[0].decode("utf-8", errors="ignore")
        except Exception:
            request_line = ""

        is_options = request_line.startswith("OPTIONS")

        if is_options:
            # ICAP 클라이언트가 capabilities 협상을 위해 OPTIONS를 보내면
            # 이 서버가 지원하는 메서드/헤더를 고정값으로 응답한다.
            resp = (
                "ICAP/1.0 200 OK\r\n"
                f"Date: {now_http_date()}\r\n"
                "Server: mock-icap\r\n"
                f"Methods: RESPMOD, REQMOD\r\n"
                f"Service: {SERVICE}\r\n"
                "ISTag: mock-icap-1\r\n"
                "Allow: 204\r\n"
                "Preview: 0\r\n"
                "Transfer-Preview: *\r\n"
                "Connection: close\r\n"
                "Encapsulated: null-body=0\r\n"
                "\r\n"
            )
            self.request.sendall(resp.encode("utf-8"))
            return

        # REQMOD/RESPMOD 본문 전체를 문자열로 보고 EICAR 시그니처 포함 여부를 검사한다.
        raw_text = data.decode("utf-8", errors="ignore")
        infected = "EICAR-STANDARD-ANTIVIRUS-TEST-FILE" in raw_text

        if infected:
            # 감염 판정 시 ICAP 200 + X-Infection-Found 헤더를 내려
            # 상위 시스템이 "바이러스 탐지"로 처리할 수 있게 만든다.
            http_payload = (
                "HTTP/1.1 200 OK\r\n"
                "Content-Type: text/plain\r\n"
                "\r\n"
                "Virus Found\r\n"
            )
            resp_headers = (
                "ICAP/1.0 200 OK\r\n"
                f"Date: {now_http_date()}\r\n"
                "Server: mock-icap\r\n"
                "ISTag: mock-icap-1\r\n"
                "X-Infection-Found: Type=0; Resolution=2; Threat=Eicar-Test-Signature\r\n"
                "Connection: close\r\n"
                f"Encapsulated: res-hdr=0, res-body={len(http_payload.encode('utf-8'))}\r\n"
                "\r\n"
            )
            self.request.sendall(resp_headers.encode("utf-8") + http_payload.encode("utf-8"))
            return

        # 감염이 없으면 204(No Content)로 "원본 변경 없음/통과"를 의미한다.
        resp = (
            "ICAP/1.0 204 No Content\r\n"
            f"Date: {now_http_date()}\r\n"
            "Server: mock-icap\r\n"
            "ISTag: mock-icap-1\r\n"
            "Connection: close\r\n"
            "Encapsulated: null-body=0\r\n"
            "\r\n"
        )
        self.request.sendall(resp.encode("utf-8"))


class ThreadingTCPServer(socketserver.ThreadingMixIn, socketserver.TCPServer):
    # 재기동 시 TIME_WAIT 소켓 때문에 bind 실패가 나지 않도록 주소 재사용을 허용한다.
    allow_reuse_address = True


if __name__ == "__main__":
    with ThreadingTCPServer((HOST, PORT), ICAPHandler) as server:
        print(f"mock-icap listening on {HOST}:{PORT}")
        server.serve_forever()
