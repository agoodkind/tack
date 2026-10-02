ARG FDB_VERSION=7.4.6

# ── builder ───────────────────────────────────────────────────────────────────
FROM golang:1.27-bookworm AS builder
ARG FDB_VERSION

# Install FDB C client library (required by CGO bindings at link time)
RUN set -eux \
    && case "$(dpkg --print-architecture)" in \
         amd64) fdb_arch=amd64 ;; \
         arm64) fdb_arch=aarch64 ;; \
         *) echo "unsupported arch: $(dpkg --print-architecture)" >&2; exit 1 ;; \
       esac \
    && curl -fsSL \
    https://github.com/apple/foundationdb/releases/download/${FDB_VERSION}/foundationdb-clients_${FDB_VERSION}-1_${fdb_arch}.deb \
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

RUN set -eux \
    && apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && case "$(dpkg --print-architecture)" in \
         amd64) fdb_arch=amd64 ;; \
         arm64) fdb_arch=aarch64 ;; \
         *) echo "unsupported arch: $(dpkg --print-architecture)" >&2; exit 1 ;; \
       esac \
    && curl -fsSL \
       https://github.com/apple/foundationdb/releases/download/${FDB_VERSION}/foundationdb-clients_${FDB_VERSION}-1_${fdb_arch}.deb \
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
