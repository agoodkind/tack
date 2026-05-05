# tack Makefile.
# Build/lint pipeline lives in go-makefile and is fetched at runtime.
# Project owns deploy/migrate/seed/backup/integration; the remote rsync
# deploy targets do not move into the central pipeline.

GO_MK_URL     := https://raw.githubusercontent.com/agoodkind/go-makefile/main/go.mk
GO_MK_API_URL := https://api.github.com/repos/agoodkind/go-makefile/contents/go.mk?ref=main
GO_MK         := .make/go.mk
GO_MK_CACHE   := $(or $(XDG_CACHE_HOME),$(HOME)/.cache)/go-makefile/go.mk
# Dev override: GO_MK_DEV_DIR=$HOME/Sites/go-makefile to iterate locally.
GO_MK_DEV_DIR ?=

# Identity. tack's internal/version uses lowercase unexported fields, so
# VPKG canonical stamping does not bind. We pre-populate GO_BUILD_LDFLAGS
# below with the lowercase -X flags; go-build.mk's ?= preserves it.
BINARY := tack
CMD    := ./cmd/server

# CGO required for FoundationDB bindings; -tags fdb gates the FDB code paths.
export CGO_ENABLED := 1
GO_BUILD_TAGS     := fdb

# Pipeline modules (skip go-release.mk: tack ships via remote rsync, not
# GoReleaser).
GO_MK_MODULES := go-build.mk

GO_MK_BOOTSTRAP := $(shell \
	mkdir -p "$(dir $(GO_MK))" "$(dir $(GO_MK_CACHE))"; \
	if [ -n "$(GO_MK_DEV_DIR)" ] && [ -f "$(GO_MK_DEV_DIR)/go.mk" ]; then \
		cp "$(GO_MK_DEV_DIR)/go.mk" "$(GO_MK)"; \
		printf '%s\n' "go.mk: using dev override $(GO_MK_DEV_DIR)/go.mk" >&2; \
	else \
		tmp="$(GO_MK).tmp"; \
		if curl -fsSL -H "Accept: application/vnd.github.raw" --connect-timeout 5 --max-time 10 "$(GO_MK_API_URL)" -o "$$tmp" || curl -fsSL --connect-timeout 5 --max-time 10 "$(GO_MK_URL)?v=$$(date +%s)" -o "$$tmp" || curl -fsSL --connect-timeout 5 --max-time 10 "$(GO_MK_URL)" -o "$$tmp"; then \
			mv "$$tmp" "$(GO_MK)"; \
			cp "$(GO_MK)" "$(GO_MK_CACHE)"; \
		elif [ -f "$(GO_MK_CACHE)" ]; then \
			rm -f "$$tmp"; \
			cp "$(GO_MK_CACHE)" "$(GO_MK)"; \
		elif [ ! -f "$(GO_MK)" ]; then \
			rm -f "$$tmp"; \
			printf '%s\n' "error: go.mk fetch failed and no cache available" >&2; \
		fi; \
	fi)

$(GO_MK):
	@mkdir -p $(dir $@)
	@if [ -n "$(GO_MK_DEV_DIR)" ] && [ -f "$(GO_MK_DEV_DIR)/go.mk" ]; then \
		cp "$(GO_MK_DEV_DIR)/go.mk" "$@"; \
		echo "go.mk: using dev override $(GO_MK_DEV_DIR)/go.mk" >&2; \
	elif curl -fsSL -H "Accept: application/vnd.github.raw" --connect-timeout 5 --max-time 10 "$(GO_MK_API_URL)" -o "$@" || curl -fsSL --connect-timeout 5 --max-time 10 "$(GO_MK_URL)?v=$$(date +%s)" -o "$@" || curl -fsSL --connect-timeout 5 --max-time 10 "$(GO_MK_URL)" -o "$@"; then \
		mkdir -p "$(dir $(GO_MK_CACHE))" && cp "$@" "$(GO_MK_CACHE)"; \
	elif [ -f "$(GO_MK_CACHE)" ]; then \
		echo "warning: go.mk fetch failed, using cached version" >&2; \
		cp "$(GO_MK_CACHE)" "$@"; \
	else \
		echo "error: go.mk fetch failed and no cache available" >&2; \
		exit 1; \
	fi

# tack version metadata. Pre-populate GO_BUILD_LDFLAGS BEFORE -include $(GO_MK)
# so go-build.mk's ?= preserves it. We also cross-stamp gklog via GKLOG_VPKG;
# go-build.mk extends GO_BUILD_LDFLAGS with the canonical Commit/Dirty/BuildTime
# stamps for that pkg.
TACK_VERSION_PKG := goodkind.io/tack/internal/version
GKLOG_VPKG       := goodkind.io/gklog/version

TACK_COMMIT     := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
TACK_DIRTY      := $(shell git diff --quiet 2>/dev/null && echo false || echo true)
TACK_TAG        := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
TACK_BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

GO_BUILD_LDFLAGS := -s -w \
	-X $(TACK_VERSION_PKG).commit=$(TACK_COMMIT) \
	-X $(TACK_VERSION_PKG).buildTime=$(TACK_BUILD_TIME) \
	-X $(TACK_VERSION_PKG).tag=$(TACK_TAG) \
	-X $(TACK_VERSION_PKG).dirty=$(TACK_DIRTY)

-include $(GO_MK)

.DEFAULT_GOAL := check

.PHONY: build-server check-gates
build-server: build

check-gates: build-check test deadcode lint-logging

# Find unreachable functions. Build first so deadcode sees the same package
# graph the build did. Test-only helpers reachable only from _test.go files
# are filtered out: deadcode does not load tests in its analysis, so they
# read as unreachable here. Anything else that lands flagged is real dead
# code and the gate fails.
.PHONY: deadcode
deadcode: build-server
	@out=$$(go run golang.org/x/tools/cmd/deadcode@latest ./... | grep -Ev \
		-e 'internal/test/integration/' \
		-e 'internal/adapters/foundationdb/keys.go:.*(SetTestPrefix|TestPrefixRange)' \
		-e 'internal/audit/' \
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
	go run $(GO_BUILD_FLAGS) $(CMD)

# Run DB migrations against DATABASE_URL.
.PHONY: migrate
migrate:
	go run $(GO_BUILD_FLAGS) $(CMD) migrate

# Seed the database with initial user/org/workspace/token.
.PHONY: seed
seed:
	go run $(GO_BUILD_FLAGS) $(CMD) seed

# Integration tests against a real FDB cluster.
#
# `make test-integration` brings up a docker-compose FDB, builds a sibling Go
# test runner image, and runs the suite inside that container on the same
# Docker network. Sibling-container tests sidestep Docker Desktop's TCP port
# forwarder, which drops FDB's connect-packet exchange on macOS. The cluster
# stays up between runs; `make test-fdb-down` cleans up.
#
# TACK_INTEGRATION inside the test runner gates the suite so a casual
# `make test` (host-side) still skips them.

.PHONY: test-fdb-up
test-fdb-up:
	./scripts/test-fdb-up.sh

.PHONY: test-fdb-down
test-fdb-down:
	./scripts/test-fdb-down.sh

.PHONY: test-integration
test-integration: test-fdb-up
	./scripts/test-integration.sh

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

# Deploy: backup first, then rsync source to CT 117, build natively on the
# server, restart. Builds exactly the checked-in dependency graph rather than
# mutating go.mod/go.sum during deploy. Uses --network host so Docker build
# can resolve DNS via the host's IPv6 nameserver.
#
# Backup-as-prereq is non-negotiable. The 2026-04-28 incident showed how
# easy it is to lose data during a deploy when the underlying infra has
# subtle persistence bugs. Every deploy now produces a snapshot first;
# if backup fails, deploy aborts before touching production state.
# Rationale lives in scripts/backup.sh and the deploy landmines memory.
#
# Override with NO_PRE_DEPLOY_BACKUP=1 only when the cluster is provably
# unhealthy and the snapshot itself would fail; document the reason.
.PHONY: deploy
ifeq ($(NO_PRE_DEPLOY_BACKUP),1)
deploy: deploy-preflight
else
deploy: deploy-preflight backup
endif
	rsync -az --delete --exclude='.git' --exclude='bin/' --exclude='dist/' --exclude='.make/' --exclude='.env' --exclude='.env.*' --exclude='.test-fdb/' . tack:/root/tack/
	ssh tack "cd /root/tack && docker build --network host \
		--build-arg COMMIT=$(TACK_COMMIT) \
		--build-arg BUILD_TIME=$(TACK_BUILD_TIME) \
		--build-arg TAG=$(TACK_TAG) \
		--build-arg DIRTY=$(TACK_DIRTY) \
		-t tack-server . && docker compose up -d --no-build app && /root/tack/scripts/host-maintenance.sh deploy-cleanup"

.PHONY: deploy-preflight
deploy-preflight:
	rsync -az scripts/host-maintenance.sh tack:/root/tack/scripts/host-maintenance.sh
	ssh tack 'chmod +x /root/tack/scripts/host-maintenance.sh && /root/tack/scripts/host-maintenance.sh deploy-preflight'

# Run a full backup on CT 117 (FDB volume + Meili volume + Yugabyte
# in-container tar + auth CSVs). Output dirs at /root/backups/tack-<TS>/.
.PHONY: backup
backup:
	rsync -az scripts/backup.sh scripts/host-maintenance.sh tack:/root/tack/scripts/
	ssh tack 'bash /root/tack/scripts/backup.sh'

.PHONY: host-maintenance-install
host-maintenance-install:
	rsync -az scripts/host-maintenance.sh tack:/root/tack/scripts/host-maintenance.sh
	ssh tack 'chmod +x /root/tack/scripts/host-maintenance.sh && /root/tack/scripts/host-maintenance.sh install-timer'

# Create the three LOGIN-capable audit derived roles (audit_writer_app,
# audit_reader_app, audit_redactor_app) and rotate their passwords from
# /root/tack/.env. Idempotent. Run once after migrate, or any time the
# audit role passwords need rotating.
.PHONY: seed-audit-roles
seed-audit-roles:
	rsync -az scripts/seed-audit-roles.sh tack:/root/tack/scripts/seed-audit-roles.sh
	ssh tack 'bash /root/tack/scripts/seed-audit-roles.sh'

# Pull the latest backup directory from CT 117 to the local Mac for
# offsite storage. Reads the timestamp from /root/backups/.latest.
.PHONY: backup-pull
backup-pull:
	@TS=$$(ssh tack 'cat /root/backups/.latest'); \
		mkdir -p ~/backups/tack/$$TS; \
		rsync -avz tack:/root/backups/tack-$$TS/ ~/backups/tack/$$TS/; \
		echo ""; \
		echo "pulled to ~/backups/tack/$$TS"
