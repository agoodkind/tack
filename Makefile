# Lint is centralized in go-makefile. Do NOT define project-local lint,
# deadcode, audit, fmt, vet, or staticcheck targets here. They duplicate
# the central pipeline and let agents bypass strict rules. Run `make help`
# for the canonical entry points (build/check/lint/fmt) and per-linter
# sub-targets (lint-golangci, lint-format, lint-gocyclo, lint-deadcode,
# staticcheck-extra). Refresh baselines via the matching *-baseline target.
#
# tack Makefile.
# Build/lint pipeline lives in go-makefile and is fetched at runtime.
# Project owns deploy/migrate/seed/backup/integration; the remote rsync
# deploy targets do not move into the central pipeline.

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
.PHONY: test-unit
test-unit:
	docker compose -f docker-compose.test.yml --profile runner build tests
	docker compose -f docker-compose.test.yml --profile runner run --rm \
	    --no-deps tests \
	    /usr/local/go/bin/go test -count=1 ./internal/ops/...

.PHONY: test-fdb-up
test-fdb-up:
	./scripts/test-fdb-up.sh

.PHONY: test-fdb-down
test-fdb-down:
	./scripts/test-fdb-down.sh

.PHONY: test-integration
test-integration: test-fdb-up
	./scripts/test-integration.sh

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
# Runs inside the tack-ops sibling container on CT 117; the tack-server image
# must already be up to date on the host before invoking this target.
.PHONY: backup
backup:
	ssh tack 'cd /root/tack && docker compose run --rm tack-ops ops backup'

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

# Build the Wave 1 audit-consumer binary into dist/audit-consumer.
.PHONY: audit-consumer
audit-consumer:
	mkdir -p dist
	go build $(GO_BUILD_FLAGS) -o dist/audit-consumer ./cmd/audit-consumer

# Pull the latest backup directory from CT 117 to the local Mac for
# offsite storage. Reads the timestamp from /root/backups/.latest.
.PHONY: backup-pull
backup-pull:
	@TS=$$(ssh tack 'cat /root/backups/.latest'); \
		mkdir -p ~/backups/tack/$$TS; \
		rsync -avz tack:/root/backups/tack-$$TS/ ~/backups/tack/$$TS/; \
		echo ""; \
		echo "pulled to ~/backups/tack/$$TS"

# Fast structural verification of the latest backup on CT 117. Catches
# the 2026-04-25 empty-tarball defect class within seconds. Exits non-zero
# when an artifact has the wrong shape; intended to run from operator
# workflows immediately after `make backup`.
.PHONY: backup-content-check
backup-content-check:
	rsync -az scripts/backup-content-check.sh tack:/root/tack/scripts/
	ssh tack 'TS=$$(cat /root/backups/.latest); /root/tack/scripts/backup-content-check.sh "/root/backups/tack-$$TS"'

# Structural inventory check of a specific backup on CT 117. Runs the
# ./server ops backup verify subcommand inside the tack-ops sibling
# container. TS is required: make backup-verify TS=20260509T232955Z
.PHONY: backup-verify
backup-verify:
ifndef TS
	$(error TS is required: make backup-verify TS=20260509T232955Z)
endif
	ssh tack 'cd /root/tack && docker compose run --rm tack-ops ops backup verify /root/backups/tack-$(TS)'
