# 1단계: Go 바이너리 빌드
FROM golang:1.22-bookworm AS builder
WORKDIR /src

# 의존성 캐시 레이어
COPY go.mod go.sum ./
RUN go mod download

# 앱 소스 복사 후 정적 링크 바이너리 생성
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/mail-admin ./cmd/mail-admin

# 2단계: 최소 런타임 이미지(distroless)로 보안/용량 최적화
FROM gcr.io/distroless/base-debian12:nonroot
WORKDIR /app
COPY --from=builder /out/mail-admin /app/mail-admin

EXPOSE 8080
ENTRYPOINT ["/app/mail-admin"]
