FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o ngrok-socks5-proxy ./cmd/ngrok-socks5-proxy/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates \
    && echo "hosts: files dns" > /etc/nsswitch.conf
COPY --from=builder /app/ngrok-socks5-proxy /usr/local/bin/
ENTRYPOINT ["ngrok-socks5-proxy"]
