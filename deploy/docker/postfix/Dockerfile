FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
    && apt-get install -y --no-install-recommends postfix ca-certificates bash \
    && rm -rf /var/lib/apt/lists/*

COPY deploy/docker/postfix/start.sh /start.sh
RUN chmod +x /start.sh

EXPOSE 25 587
ENTRYPOINT ["/start.sh"]
