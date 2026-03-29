FROM golang:1.22-bookworm AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/mail-admin ./cmd/mail-admin

FROM gcr.io/distroless/base-debian12:nonroot
WORKDIR /app
COPY --from=builder /out/mail-admin /app/mail-admin

EXPOSE 8080
ENTRYPOINT ["/app/mail-admin"]
