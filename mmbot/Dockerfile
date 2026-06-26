# syntax=docker/dockerfile:1
#
# flip7bot (Go / Mattermost) image.
#
# Multi-stage build:
#   - builder: compiles a fully static (CGO_ENABLED=0) binary.
#   - runtime: a minimal alpine image running as a non-root user.
#
# SQLite migrations are embedded in the binary (embed.FS), so no migration
# files need to be copied into the runtime image.

# --- Build stage ----------------------------------------------------------
FROM golang:1.26.4-alpine AS builder

WORKDIR /src

# Module-cache layering: download deps before copying source so the module
# layer is only rebuilt when go.mod / go.sum change.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source and build a static binary.
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mmbot ./cmd/mmbot

# --- Runtime stage --------------------------------------------------------
FROM alpine:3.24 AS runtime

# ca-certificates: TLS roots for outbound calls to Mattermost.
# wget: used by the compose healthcheck to probe /healthz.
RUN apk add --no-cache ca-certificates wget \
    && addgroup -S bot \
    && adduser -S -G bot bot

WORKDIR /app

COPY --from=builder /out/mmbot ./mmbot

USER bot

CMD ["./mmbot"]
