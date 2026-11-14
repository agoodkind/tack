# go-makefile owns build and lint. Do not add project-local lint targets.
# Run make help for its entry points and baseline targets.

BINARY := tack
CMD    := ./cmd/server

# CGO required for FoundationDB bindings; -tags fdb gates the FDB code paths.
export CGO_ENABLED := 1
GO_BUILD_TAGS     := fdb

# Pipeline modules (skip go-release.mk: tack ships prebuilt container images
# from CI, not GoReleaser).
GO_MK_MODULES := go-build.mk

# CI runs the integration package separately through test-store-host-shard.
GO_TEST_INTEGRATION_PACKAGE := goodkind.io/tack/internal/test/integration
ifneq ($(filter test,$(MAKECMDGOALS)),)
GO_TEST_PACKAGES := $(filter-out $(GO_TEST_INTEGRATION_PACKAGE) $(GO_TEST_INTEGRATION_PACKAGE)/%,$(shell go list ./...))
ifeq ($(strip $(GO_TEST_PACKAGES)),)
$(error make test could not select any packages. Check go list ./... output and the integration package filter.)
endif
endif
GO_TEST_TARGETS := -timeout 20m $(GO_TEST_PACKAGES)

# Reject deploy before go-build.mk can add its install prerequisite.
ifneq ($(filter deploy,$(MAKECMDGOALS)),)
$(error make deploy is retired: use './server ops deploy' for app images or Ansible deploy-tack.yml for the full stack)
endif

include bootstrap.mk

# Reject backfills after their clispec.Lifetime removal day (TACK-512).
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
	go run $(GO_BUILD_FLAGS) $(CMD) migrate --execute

# Seed the database with initial user/org/workspace/token.
.PHONY: seed
seed:
	go run $(GO_BUILD_FLAGS) $(CMD) seed

include test.mk

# Keep the FoundationDB server image, client library, Go bindings, and
# fdb.APIVersion compatible. Docker builds inherit FDB_VERSION.
FDB_VERSION ?= 7.4.6
FDB_BINDINGS_VERSION := v0.0.0-20250923185926-685eda6efef7

.PHONY: update-deps
update-deps:
	go get -u ./...
	go get goodkind.io/gklog@main
	go get github.com/apple/foundationdb/bindings/go@$(FDB_BINDINGS_VERSION)
	go mod tidy

# Set VERSION and update FDB_BINDINGS_VERSION to the matching release line.
.PHONY: update-fdb
update-fdb:
	@if [ -z "$(VERSION)" ]; then echo "usage: make update-fdb VERSION=7.4.7" >&2; exit 1; fi
	@echo "Bumping FDB to $(VERSION) in Dockerfile, docker-compose, docker-compose.test."
	@sed -i.bak "s/^ARG FDB_VERSION=.*/ARG FDB_VERSION=$(VERSION)/" Dockerfile && rm Dockerfile.bak
	@sed -i.bak "s/^FDB_VERSION ?= .*/FDB_VERSION ?= $(VERSION)/" Makefile && rm Makefile.bak
	@echo "Reminder: hand-edit FDB_BINDINGS_VERSION in this Makefile to match"
	@echo "and update the fdb.APIVersion call in internal/adapters/foundationdb/client.go."

# Create or rotate audit LOGIN roles after migrations or password changes.
# tack-ops reads the audit passwords and DATABASE_URL from the rendered .env.
.PHONY: seed-audit-roles
seed-audit-roles:
	ssh tack 'cd /root/tack && docker compose run --rm tack-ops /server ops audit seed-roles'

.PHONY: audit-consumer
audit-consumer:
	mkdir -p dist
	go build $(GO_BUILD_FLAGS) -o dist/audit-consumer ./cmd/audit-consumer
