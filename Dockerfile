# syntax=docker/dockerfile:1

# ──────────────────────────────────────────────────────────────────────────────
# Stage 1 – Build
# Uses the official Go image to compile a fully-static binary.
# modernc.org/sqlite is a pure-Go SQLite port, so CGO_ENABLED=0 works fine.
# ──────────────────────────────────────────────────────────────────────────────
FROM golang:1.24-bookworm AS builder

WORKDIR /src

# Cache dependencies before copying the full source tree.
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /nxt-opds .

# ──────────────────────────────────────────────────────────────────────────────
# Stage 2 – Runtime
# Minimal Debian-slim image: has CA certs and a shell for debugging.
# Includes Node.js + Claude Code CLI so AI-assisted workflows are available
# inside the container (e.g. for operators via `docker exec`).
# ──────────────────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim AS runtime

# Install CA certificates, Node.js (LTS via NodeSource) and Claude Code CLI.
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates curl && \
    curl -fsSL https://deb.nodesource.com/setup_lts.x | bash - && \
    apt-get install -y --no-install-recommends nodejs && \
    npm install -g @anthropic-ai/claude-code && \
    rm -rf /var/lib/apt/lists/*

# Run as a non-root user.
RUN useradd -u 1000 -m -d /app nxt-opds
USER nxt-opds

# Copy the compiled binary.
COPY --from=builder /nxt-opds /app/nxt-opds

# Books are stored on a mounted volume at /data/books.
VOLUME ["/data/books"]

WORKDIR /app

EXPOSE 8080

ENV LISTEN_ADDR=:8080
ENV BOOKS_DIR=/data/books
ENV BACKEND=sqlite

ENTRYPOINT ["/app/nxt-opds"]
