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

# Run unit tests for the ops package (and any other package without
# integration deps) inside the test runner image. The compose file already
# bind-mounts the source tree to /src so edits are visible without a rebuild.
#
# The postgres adapter is here because its pool tests stand up loopback
# listeners that stand in for a lost ledger guest; they need no cluster.
#
# The audit package is here for the export and verify scale tests. They are the
# gate on the compliance bundle's memory footprint, and a footprint assertion
# nothing runs is not a gate. Its database-backed tests skip on an unset DSN, so
# they cost nothing here.
.PHONY: test-unit
test-unit:
	docker compose -f docker-compose.test.yml --profile runner build tests
	docker compose -f docker-compose.test.yml --profile runner run --rm \
	    --no-deps --entrypoint /usr/local/go/bin/go tests \
	    test -count=1 ./internal/ops/... ./internal/adapters/postgres/... ./internal/audit/...

.PHONY: test-fdb-up
test-fdb-up:
	./scripts/test-fdb-up.sh

.PHONY: test-fdb-down
test-fdb-down:
	./scripts/test-fdb-down.sh

.PHONY: test-integration
test-integration: test-fdb-up
	./scripts/test-integration.sh

# Database-gated audit tests (chain append, outbox, token lifecycle) against
# the test YugabyteDB alone, migrated and run inside the sibling runner with
# AUDIT_CHAIN_TEST_DSN set. Needs no FoundationDB, so it runs on hosts where
# the FDB image cannot. The service is pinned to the amd64 build (TACK-459).
.PHONY: test-audit-db
test-audit-db:
	./scripts/test-audit-db.sh

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
