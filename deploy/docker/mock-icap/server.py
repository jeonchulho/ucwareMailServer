import socketserver
from datetime import datetime, timezone

HOST = "0.0.0.0"
PORT = 1344
SERVICE = "avscan"


def now_http_date() -> str:
    return datetime.now(timezone.utc).strftime("%a, %d %b %Y %H:%M:%S GMT")


class ICAPHandler(socketserver.BaseRequestHandler):
    def handle(self) -> None:
        data = b""
        self.request.settimeout(3)
        while True:
            try:
                chunk = self.request.recv(4096)
            except Exception:
                break
            if not chunk:
                break
            data += chunk
            if b"\r\n0\r\n\r\n" in data or len(data) > 1024 * 1024:
                break

        if not data:
            return

        try:
            request_line = data.split(b"\r\n", 1)[0].decode("utf-8", errors="ignore")
        except Exception:
            request_line = ""

        is_options = request_line.startswith("OPTIONS")

        if is_options:
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

        raw_text = data.decode("utf-8", errors="ignore")
        infected = "EICAR-STANDARD-ANTIVIRUS-TEST-FILE" in raw_text

        if infected:
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
    allow_reuse_address = True


if __name__ == "__main__":
    with ThreadingTCPServer((HOST, PORT), ICAPHandler) as server:
        print(f"mock-icap listening on {HOST}:{PORT}")
        server.serve_forever()
