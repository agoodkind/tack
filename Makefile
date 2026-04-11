include build/mk/go.mk

# go.mk provides: lint, fmt, vet, test, check, govulncheck

# Noop build (no CGO, FDB is stub). Use for local dev and CI typecheck.
.PHONY: build
build:
	go build ./...

# Production build: real FoundationDB adapter (CGO, Linux only).
# Requires foundationdb-clients 7.4.x on the build host.
.PHONY: build-fdb
build-fdb:
	CGO_ENABLED=1 go build -tags fdb -trimpath -ldflags="-s -w" -o bin/server ./cmd/server

# Run the server locally (noop FDB stub, no CGO required).
.PHONY: run
run:
	go run ./cmd/server

# Run DB migrations against DATABASE_URL.
.PHONY: migrate
migrate:
	go run ./cmd/server migrate

# Seed the database with initial user/org/workspace/token.
.PHONY: seed
seed:
	go run ./cmd/server seed
