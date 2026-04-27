# --- Build stage ---
FROM rust:1.86-alpine AS builder
RUN apk add --no-cache musl-dev pkgconfig openssl-dev openssl-libs-static
WORKDIR /app
COPY Cargo.toml Cargo.lock ./
RUN mkdir src && echo "fn main(){}" > src/main.rs && cargo build --release && rm -rf src
COPY src ./src
COPY migrations ./migrations
RUN touch src/main.rs && cargo build --release

# --- Runtime stage ---
FROM alpine:3.20 AS runtime
RUN apk add --no-cache ca-certificates procps su-exec
RUN addgroup -S bot && adduser -S bot -G bot
WORKDIR /app
COPY --from=builder /app/target/release/flip7bot ./flip7bot
COPY migrations ./migrations
COPY entrypoint.sh ./entrypoint.sh
RUN chmod +x entrypoint.sh
CMD ["./entrypoint.sh"]
