# builder
FROM golang:1.25 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/caddy-watcher

# final
FROM alpine:3.18
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/caddy-watcher /usr/local/bin/caddy-watcher
ENTRYPOINT ["/usr/local/bin/caddy-watcher"]