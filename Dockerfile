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
# Distroless static image: just the bare minimum needed to run a static Go
# binary (CA bundle, /etc/passwd, tzdata).  No shell, no package manager,
# no Node.js — that bloat lived only to support the Claude Code CLI which is
# now in Dockerfile.dev for operator/devcontainer workflows.
# Resulting image size: ~25 MB instead of ~450 MB.
# ──────────────────────────────────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

# Copy the compiled binary.
COPY --from=builder /nxt-opds /app/nxt-opds

# Books are stored on a mounted volume at /data/books.
VOLUME ["/data/books"]

WORKDIR /app

EXPOSE 8080

ENV LISTEN_ADDR=:8080
ENV BOOKS_DIR=/data/books
ENV BACKEND=sqlite

# distroless/static "nonroot" runs as uid 65532; matches the previous Debian
# image's behaviour of dropping privileges.
ENTRYPOINT ["/app/nxt-opds"]
