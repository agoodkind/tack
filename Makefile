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

# Build metadata injected via ldflags.
VERSION_PKG := goodkind.io/tack/internal/version
COMMIT      := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
DIRTY       := $(shell git diff --quiet 2>/dev/null && echo false || echo true)
TAG         := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_TIME  := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GKLOG_VERSION_PKG := goodkind.io/gklog/version
LDFLAGS     := -s -w \
	-X $(VERSION_PKG).commit=$(COMMIT) \
	-X $(VERSION_PKG).buildTime=$(BUILD_TIME) \
	-X $(VERSION_PKG).tag=$(TAG) \
	-X $(VERSION_PKG).dirty=$(DIRTY) \
	-X $(GKLOG_VERSION_PKG).Commit=$(COMMIT) \
	-X $(GKLOG_VERSION_PKG).Dirty=$(DIRTY) \
	-X $(GKLOG_VERSION_PKG).BuildTime=$(BUILD_TIME)

# FDB is always required. CGO_ENABLED=1 for the FDB C bindings.
.PHONY: build
build:
	CGO_ENABLED=1 go build -ldflags="$(LDFLAGS)" -o bin/server ./cmd/server

# check runs every gate the project enforces: build, vet, lint, unit tests,
# vulnerability check, the staticcheck extras the clyde repo uses, deadcode,
# and the project-specific structured-logging discipline. Overrides go.mk's
# check on purpose so all gates land in one target.
.PHONY: check
check: build vet lint test govulncheck staticcheck deadcode lint-logging

# Static analysis with the clyde-staticcheck analyzer set. Same flags mwan/go
# uses so all three Go services in this monorepo enforce the same extras.
# clyde-staticcheck lives in the clyde repo because the analyzer code is owned
# there; tack consumes it by building that binary on demand. Override
# STATICCHECK_BUILD_REPO if your clyde checkout lives elsewhere.
STATICCHECK_BUILD_REPO ?= $(HOME)/Sites/clyde-dev/clyde
STATICCHECK_BUILD_PKG  ?= ./cmd/clyde-staticcheck
# Same analyzers clyde and mwan run, minus -no_any_or_empty_interface.
# Tack's MCP boundary takes arbitrary JSON tool inputs through the
# google/jsonschema-go path; that boundary is fundamentally any-typed and
# cannot be replaced with a closed enum. The other analyzers still apply.
STATICCHECK_FLAGS      ?= -slog_error_without_err -banned_direct_output \
	-hot_loop_info_log -missing_boundary_log
STATICCHECK_BIN        := $(shell go env GOPATH)/bin/clyde-staticcheck

.PHONY: staticcheck
staticcheck:
	@if [ ! -d "$(STATICCHECK_BUILD_REPO)" ]; then \
		echo "skipping staticcheck: $(STATICCHECK_BUILD_REPO) not found" >&2; \
		exit 0; \
	fi
	@cd $(STATICCHECK_BUILD_REPO) && go build -o $(STATICCHECK_BIN) $(STATICCHECK_BUILD_PKG)
	@$(STATICCHECK_BIN) $(STATICCHECK_FLAGS) ./...

# Find unreachable functions. Build first so deadcode sees the same package
# graph the build did. Test-only helpers reachable only from _test.go files
# are filtered out: deadcode does not load tests in its analysis, so they
# read as unreachable here. Anything else that lands flagged is real dead
# code and the gate fails.
.PHONY: deadcode
deadcode: build
	@out=$$(go run golang.org/x/tools/cmd/deadcode@latest ./... | grep -Ev \
		-e 'internal/test/integration/' \
		-e 'internal/adapters/foundationdb/keys.go:.*(SetTestPrefix|TestPrefixRange)' \
		); \
	if [ -n "$$out" ]; then \
		echo "$$out"; exit 1; \
	fi

# Informational complexity + vulnerability scan. Not part of check by default
# because gocyclo can fire on legitimate code; keep it as a separate signal.
.PHONY: audit
audit:
	@echo "=== Cyclomatic complexity (>15) ==="
	@go run github.com/fzipp/gocyclo/cmd/gocyclo@latest -over 15 . || true
	@echo
	@echo "=== Vulnerability check ==="
	@go run golang.org/x/vuln/cmd/govulncheck@latest ./... || true

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

# Integration tests against a real FDB cluster.
#
# By default `make test-integration` brings up a docker-compose FDB on
# 127.0.0.1:4500, runs the suite, and leaves the cluster up so subsequent
# runs are fast. Use `make test-fdb-down` to clean up.
#
# Override FDB_CLUSTER_FILE to point at a different cluster (e.g. the local
# launchd-managed FDB at /usr/local/etc/foundationdb/fdb.cluster on macOS, or
# /etc/foundationdb/fdb.cluster on Linux). TACK_INTEGRATION gates the tests
# so a casual `make test` skips them.
FDB_CLUSTER_FILE ?= $(PWD)/.test-fdb/fdb.cluster

.PHONY: test-fdb-up
test-fdb-up:
	./scripts/test-fdb-up.sh

.PHONY: test-fdb-down
test-fdb-down:
	./scripts/test-fdb-down.sh

.PHONY: test-integration
test-integration: test-fdb-up
	CGO_ENABLED=1 TACK_INTEGRATION=1 FDB_CLUSTER_FILE=$(FDB_CLUSTER_FILE) go test -v -count=1 ./internal/test/integration/...

# lint-logging: enforce structured-logging discipline per the plan in
# /Users/agoodkind/.claude/plans/indexed-growing-snowglobe.md.
#
# Banned patterns in production code (non-cmd, non-test):
#   - fmt.Print* for diagnostics: route through slog instead
#   - stdlib log.Print*/log.Fatal*/log.Panic*: replaced by gklog/slog
#
# cmd/ is exempt because user-facing CLI output is allowed there. Tests are
# exempt because t.Logf and friends are fine. Generated code under gen/ is
# exempt by path.
.PHONY: lint-logging
lint-logging:
	@./scripts/lint-logging.sh

# Bump every direct and indirect dependency to its latest minor/patch
# version, plus track the latest main commit of any goodkind.io/* module
# we own. Tack does not pin to tagged releases; freshness wins.
#
# `go get -u ./...` covers everything in go.mod. `go get goodkind.io/gklog@main`
# explicitly resubscribes to gklog's main branch in case go's pseudo-version
# resolver decided a stale commit was acceptable.
#
# FoundationDB pieces must stay in lockstep across three places:
#   - FDB_VERSION below is the cluster server image (used by docker-compose
#     and docker-compose.test via ${FDB_VERSION}, plus the Dockerfile build
#     arg for the foundationdb-clients C library install).
#   - FDB_BINDINGS_VERSION pins the Go bindings; the bindings carry the C
#     header and must match the installed C library. Bumping past the local
#     client breaks the build with "Requested API version requires a newer
#     version of this header".
#   - The fdb.APIVersion(...) call in internal/adapters/foundationdb/client.go
#     must be set to the API version the cluster supports.
#
# 7.4.6 is the newest FoundationDB release as of April 2026 (7.5 has not
# shipped). Use make update-fdb VERSION=x.y.z to bump the cluster image and
# Go bindings together; the Dockerfile inherits FDB_VERSION via build args.
FDB_VERSION ?= 7.4.6
FDB_BINDINGS_VERSION := v0.0.0-20250923185926-685eda6efef7

.PHONY: update-deps
update-deps:
	go get -u ./...
	go get goodkind.io/gklog@main
	go get github.com/apple/foundationdb/bindings/go@$(FDB_BINDINGS_VERSION)
	go mod tidy

# Bump FDB cluster image + bindings together. Pass the desired cluster
# version: make update-fdb VERSION=7.4.7. Edit FDB_BINDINGS_VERSION above
# in the same PR to a bindings commit that targets the same release line.
.PHONY: update-fdb
update-fdb:
	@if [ -z "$(VERSION)" ]; then echo "usage: make update-fdb VERSION=7.4.7" >&2; exit 1; fi
	@echo "Bumping FDB to $(VERSION) in Dockerfile, docker-compose, docker-compose.test."
	@sed -i.bak "s/^ARG FDB_VERSION=.*/ARG FDB_VERSION=$(VERSION)/" Dockerfile && rm Dockerfile.bak
	@sed -i.bak "s/^FDB_VERSION ?= .*/FDB_VERSION ?= $(VERSION)/" Makefile && rm Makefile.bak
	@echo "Reminder: hand-edit FDB_BINDINGS_VERSION in this Makefile to match"
	@echo "and update the fdb.APIVersion call in internal/adapters/foundationdb/client.go."

# Deploy: rsync source to CT 117, build natively on the server, restart.
# Always bumps deps first so every deployed build carries the latest of
# everything in go.mod.
# Uses --network host so Docker build can resolve DNS via the host's IPv6 nameserver.
.PHONY: deploy
deploy: update-deps
	rsync -az --delete --exclude='.git' --exclude='bin/' . tack:/root/tack/
	ssh tack "cd /root/tack && docker build --network host \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_TIME=$(BUILD_TIME) \
		--build-arg TAG=$(TAG) \
		--build-arg DIRTY=$(DIRTY) \
		-t tack-server . && docker compose up -d --no-build app"
