FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod main.go ./
COPY internal/ ./internal/
COPY config/seed.json ./config/seed.json
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/location-bot .
FROM alpine:3.21
RUN apk --no-cache add ca-certificates tzdata && addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=builder /out/location-bot /usr/local/bin/location-bot
RUN mkdir -p /app/runtime /app/config && chown -R app:app /app/runtime /app/config
USER app
ENTRYPOINT ["/usr/local/bin/location-bot"]
