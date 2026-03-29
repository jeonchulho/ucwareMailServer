FROM python:3.12-alpine

# 간단한 mock ICAP 서버 실행 디렉토리
WORKDIR /app
COPY server.py /app/server.py

# ICAP 포트
EXPOSE 1344
# 개발/테스트용 단일 프로세스 실행
CMD ["python", "/app/server.py"]
