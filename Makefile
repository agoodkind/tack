GO_MK_URL   := https://raw.githubusercontent.com/agoodkind/go-makefile/main/go.mk
GO_MK       := .make/go.mk
GO_MK_CACHE := $(HOME)/.cache/go-makefile/go.mk

$(GO_MK):
	@mkdir -p $(dir $@)
	@if curl -fsSL --connect-timeout 5 --max-time 10 "$(GO_MK_URL)" -o "$@"; then \
		mkdir -p "$(dir $(GO_MK_CACHE))" && cp "$@" "$(GO_MK_CACHE)"; \
	elif [ -f "$(GO_MK_CACHE)" ]; then \
		echo "warning: go.mk fetch failed, using cached version" >&2; \
		cp "$(GO_MK_CACHE)" "$@"; \
	else \
		echo "error: go.mk fetch failed and no cache available" >&2; \
		exit 1; \
	fi

# Only include go.mk if it already exists to avoid triggering fetch
ifeq ($(wildcard $(GO_MK)),$(GO_MK))
-include $(GO_MK)
endif

.DEFAULT_GOAL := check

# Fetch or update go.mk explicitly.
.PHONY: update-go-mk
update-go-mk:
	@mkdir -p "$(dir $(GO_MK))"
	@if curl -fsSL --connect-timeout 5 --max-time 10 "$(GO_MK_URL)" -o "$(GO_MK)"; then \
		mkdir -p "$(dir $(GO_MK_CACHE))" && cp "$(GO_MK)" "$(GO_MK_CACHE)"; \
		echo "go.mk updated"; \
	else \
		echo "error: go.mk fetch failed" >&2; \
		exit 1; \
	fi

# Noop build (no CGO, FDB is stub). Use for local dev and CI typecheck.
.PHONY: build
build:
	go build ./cmd/server

# Production build: real FoundationDB adapter (CGO).
# Requires foundationdb-clients 7.4.x on the build host.
# The FDB Go bindings (fdb_darwin.go / fdb_linux.go) supply their own cgo flags.
.PHONY: build-fdb
build-fdb:
	CGO_ENABLED=1 go build -tags fdb -o bin/server ./cmd/server

# Lint all code including FDB-tagged files.
.PHONY: lint-fdb
lint-fdb:
	CGO_ENABLED=1 golangci-lint run --build-tags fdb ./...

# Standard lint, vet, test, and check targets
.PHONY: lint vet test check
lint:
	golangci-lint run ./...

vet:
	go vet ./...

test:
	go test ./...

check: build vet lint test

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
