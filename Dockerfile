# syntax=docker/dockerfile:1.5

FROM golang:1.22-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# go-sqlite3 uses cgo.
RUN CGO_ENABLED=1 go build -o /out/wallet_monitor .

FROM debian:bookworm-slim

RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates \
  && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=build /out/wallet_monitor /app/wallet_monitor

EXPOSE 8080

ENTRYPOINT ["/app/wallet_monitor"]

