GO_MK_URL   := https://raw.githubusercontent.com/agoodkind/go-makefile/main/go.mk
GO_MK       := .make/go.mk
GO_MK_CACHE := $(HOME)/.cache/go-makefile/go.mk

$(GO_MK):
	@[ -f "$@" ] && exit 0; \
	mkdir -p $(dir $@); \
	if curl -fsSL --connect-timeout 5 --max-time 10 "$(GO_MK_URL)" -o "$@"; then \
		mkdir -p "$(dir $(GO_MK_CACHE))" && cp "$@" "$(GO_MK_CACHE)"; \
	elif [ -f "$(GO_MK_CACHE)" ]; then \
		echo "warning: go.mk fetch failed, using cached version" >&2; \
		cp "$(GO_MK_CACHE)" "$@"; \
	else \
		echo "warning: go.mk not available, using local targets only" >&2; \
	fi

-include $(GO_MK)

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

# FDB is always required. CGO_ENABLED=1 for the FDB C bindings.
.PHONY: build
build:
	CGO_ENABLED=1 go build -o bin/server ./cmd/server

.PHONY: check
check: build vet lint test

.PHONY: run
run:
	CGO_ENABLED=1 go run ./cmd/server

# Run DB migrations against DATABASE_URL.
.PHONY: migrate
migrate:
	CGO_ENABLED=1 go run ./cmd/server migrate

# Seed the database with initial user/org/workspace/token.
.PHONY: seed
seed:
	CGO_ENABLED=1 go run ./cmd/server seed

# Deploy: rsync source to CT 117, build natively on the server, restart.
# Uses --network host so Docker build can resolve DNS via the host's IPv6 nameserver.
.PHONY: deploy
deploy:
	rsync -az --delete --exclude='.git' --exclude='bin/' . tack:/root/tack/
	ssh tack 'cd /root/tack && docker build --network host -t tack-server . && docker compose up -d --no-build app'
