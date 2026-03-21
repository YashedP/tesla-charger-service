FROM golang:1.24-alpine AS builder
WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/tesla-charger-service ./cmd/server

FROM alpine:3.20
RUN adduser -D -u 10001 app
WORKDIR /app

COPY --from=builder /out/tesla-charger-service /app/tesla-charger-service
COPY scripts /app/scripts

RUN mkdir -p /app/data /app/secrets && chown -R app:app /app
USER app

ENTRYPOINT ["/app/tesla-charger-service"]
