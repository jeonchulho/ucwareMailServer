# milter 프록시 리스닝 소켓
bind_socket = "0.0.0.0:11332";
# Postfix milter 프로토콜 활성화
milter = yes;
# milter 세션 타임아웃
timeout = 120s;
