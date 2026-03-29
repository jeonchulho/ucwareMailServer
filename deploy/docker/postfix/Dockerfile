FROM ubuntu:24.04

# 패키지 설치 시 비대화형 모드 사용
ENV DEBIAN_FRONTEND=noninteractive

# postfix 실행에 필요한 최소 패키지 설치
RUN apt-get update \
    && apt-get install -y --no-install-recommends postfix ca-certificates bash \
    && rm -rf /var/lib/apt/lists/*

# 시작 스크립트 복사 및 실행 권한 부여
COPY deploy/docker/postfix/start.sh /start.sh
RUN chmod +x /start.sh

# 25: SMTP, 587: Submission
EXPOSE 25 587
ENTRYPOINT ["/start.sh"]
