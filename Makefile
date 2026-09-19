# Lint is centralized in go-makefile. Do NOT define project-local lint,
# deadcode, audit, fmt, vet, or staticcheck targets here. They duplicate
# the central pipeline and let agents bypass strict rules. Run `make help`
# for the canonical entry points (build/check/lint/fmt) and per-linter
# sub-targets (lint-golangci, lint-format, lint-gocyclo, lint-deadcode,
# staticcheck-extra). Refresh baselines via the matching *-baseline target.
#
# tack Makefile.
# Build/lint pipeline lives in go-makefile and is fetched at runtime.
# Project owns migrate/seed/backup/integration; these do not move into the
# central pipeline.

# Identity. tack's internal/version uses lowercase unexported fields, so
# VPKG canonical stamping does not bind. We pre-populate GO_BUILD_LDFLAGS
# below with the lowercase -X flags; go-build.mk's ?= preserves it.
BINARY := tack
CMD    := ./cmd/server

# CGO required for FoundationDB bindings; -tags fdb gates the FDB code paths.
export CGO_ENABLED := 1
GO_BUILD_TAGS     := fdb

# Pipeline modules (skip go-release.mk: tack ships prebuilt container images
# from CI, not GoReleaser).
GO_MK_MODULES := go-build.mk

# `make deploy` is retired. The tack rsync-to-host deploy is gone; app-image
# updates use `./server ops deploy` and full-stack deploys use Ansible
# deploy-tack.yml. Abort at parse time when deploy is a goal, before the
# inherited go-build.mk `deploy: install` prerequisite is even read, so no
# install can run (including under parallel make).
ifneq ($(filter deploy,$(MAKECMDGOALS)),)
$(error make deploy is retired: use './server ops deploy' for app images or Ansible deploy-tack.yml for the full stack)
endif

include bootstrap.mk

# A backfill declares a clispec.Lifetime with a removal day. The build refuses
# once that day has passed, so a finished backfill cannot stay in the tree
# (TACK-512). This is a project gate, not a copy of a central linter.
.PHONY: backfill-expiry
backfill-expiry:
	go run ./cmd/backfillcheck

build: backfill-expiry

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

# Tests run inside the test runner image (docker-compose.test.yml), which
# bind-mounts the source tree to /src and the Docker socket. A test that needs
# FoundationDB or the ledger starts it through internal/testenv, which gives
# each test binary its own engine containers and removes them when the binary
# exits; `make test-env-down` removes any a killed binary left. Only
# `go test -short` skips the store-backed tests.

# The ops, postgres adapter, and audit packages. The audit package carries the
# export and verify scale tests, the gate on the compliance bundle's memory
# footprint, and a footprint assertion nothing runs is not a gate.
.PHONY: test-unit
test-unit:
	docker compose -f docker-compose.test.yml --profile runner build tests
	docker compose -f docker-compose.test.yml --profile runner run --rm tests \
	    test -count=1 -timeout 30m ./internal/ops/... ./internal/adapters/postgres/... ./internal/audit/... ./internal/service/...

# Every package whose tests reach FoundationDB or the ledger, and the go test
# arguments that run them. test-store-host runs them on the current host,
# which needs the FoundationDB client library and a reachable Docker daemon;
# the CI integration job runs it. test-integration runs them in the runner.
TEST_STORE_PACKAGES := ./internal/test/integration/... ./internal/adapters/foundationdb/... \
	./internal/audit/... ./internal/ops/... ./internal/datagen/... ./cmd/server/...
TEST_STORE_ARGS := -count=1 -timeout 30m -v $(TEST_STORE_PACKAGES)

.PHONY: test-store-host
test-store-host:
	go test $(TEST_STORE_ARGS)

.PHONY: test-integration
test-integration:
	docker compose -f docker-compose.test.yml --profile runner build tests
	docker compose -f docker-compose.test.yml --profile runner run --rm tests \
	    test $(TEST_STORE_ARGS)

# Remove every engine internal/testenv or cmd/testenv started, and their
# network. A test binary removes its own engines when it exits normally; this
# clears what a killed binary or `go run ./cmd/testenv ledger` left running.
.PHONY: test-env-down
test-env-down:
	go run ./cmd/testenv down

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

# Create or rotate the three LOGIN audit roles (tack_audit_writer,
# tack_audit_reader, tack_audit_redactor), the names the app DSNs authenticate
# as, each granting its base role from migration 002. Idempotent. Runs the Go
# ops command inside the tack-ops container, which reads the audit passwords and
# DATABASE_URL from the rendered .env. Run once after migrate, or any time the
# audit role passwords need rotating.
.PHONY: seed-audit-roles
seed-audit-roles:
	ssh tack 'cd /root/tack && docker compose run --rm tack-ops /server ops audit seed-roles'

# Build the Wave 1 audit-consumer binary into dist/audit-consumer.
.PHONY: audit-consumer
audit-consumer:
	mkdir -p dist
	go build $(GO_BUILD_FLAGS) -o dist/audit-consumer ./cmd/audit-consumer
