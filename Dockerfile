FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /autoscaler ./cmd/autoscaler

FROM alpine:3.20

RUN apk add --no-cache ca-certificates curl && \
    curl -fsSL https://github.com/siderolabs/talos/releases/latest/download/talosctl-linux-amd64 -o /usr/local/bin/talosctl && \
    chmod +x /usr/local/bin/talosctl && \
    apk del curl

COPY --from=builder /autoscaler /usr/local/bin/autoscaler

USER 65534
ENTRYPOINT ["autoscaler"]
