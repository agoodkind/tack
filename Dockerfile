ARG FDB_VERSION=7.3.27

# ── builder ───────────────────────────────────────────────────────────────────
FROM golang:1.26-bookworm AS builder
ARG FDB_VERSION

# Install FDB C client library (required by CGO bindings at link time)
RUN curl -fsSL \
    https://github.com/apple/foundationdb/releases/download/${FDB_VERSION}/foundationdb-clients_${FDB_VERSION}-1_amd64.deb \
    -o /tmp/fdb-clients.deb \
    && dpkg -i /tmp/fdb-clients.deb \
    && rm /tmp/fdb-clients.deb

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /bin/server ./cmd/server

# ── runtime ───────────────────────────────────────────────────────────────────
FROM debian:bookworm-slim AS runtime
ARG FDB_VERSION

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && curl -fsSL \
       https://github.com/apple/foundationdb/releases/download/${FDB_VERSION}/foundationdb-clients_${FDB_VERSION}-1_amd64.deb \
       -o /tmp/fdb-clients.deb \
    && dpkg -i /tmp/fdb-clients.deb \
    && rm /tmp/fdb-clients.deb \
    && apt-get remove -y curl && apt-get autoremove -y \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /bin/server /server
ENTRYPOINT ["/server"]
