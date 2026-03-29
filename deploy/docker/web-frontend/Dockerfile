# 1단계: 웹 프론트엔드 Go 바이너리 빌드
FROM golang:1.25 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/web-frontend ./cmd/web-frontend

# 2단계: distroless 런타임 이미지
FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=builder /out/web-frontend /app/web-frontend
EXPOSE 8090
ENTRYPOINT ["/app/web-frontend"]
