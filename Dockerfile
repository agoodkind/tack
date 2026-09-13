ARG FDB_VERSION=7.4.6

# ── builder ───────────────────────────────────────────────────────────────────
FROM golang:1.27-bookworm AS builder
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

ARG COMMIT=unknown
ARG BUILD_TIME=unknown
ARG TAG=dev
ARG DIRTY=false
RUN CGO_ENABLED=1 go build -tags fdb -trimpath \
    -ldflags="-s -w \
        -X goodkind.io/tack/internal/version.commit=${COMMIT} \
        -X goodkind.io/tack/internal/version.buildTime=${BUILD_TIME} \
        -X goodkind.io/tack/internal/version.tag=${TAG} \
        -X goodkind.io/tack/internal/version.dirty=${DIRTY} \
        -X goodkind.io/gklog/version.Commit=${COMMIT} \
        -X goodkind.io/gklog/version.Dirty=${DIRTY} \
        -X goodkind.io/gklog/version.BuildTime=${BUILD_TIME}" \
    -o /bin/tack ./cmd/server

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

# The binary is `tack`; `/server` stays a symlink to it so existing deploy and
# compose invocations that call `/server ...` (and the `/server` entrypoint)
# keep working unchanged.
COPY --from=builder /bin/tack /usr/local/bin/tack
RUN ln -s /usr/local/bin/tack /server
ENTRYPOINT ["/server"]
