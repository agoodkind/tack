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

# `make test` passes these words to `go test`. Without a -timeout flag,
# each package stops after the Go default of 10 minutes. The package list
# leaves out `./internal/test/integration/...` because the CI integration job
# runs that package through `test-store-host`.
GO_TEST_INTEGRATION_PACKAGE := goodkind.io/tack/internal/test/integration
ifneq ($(filter test,$(MAKECMDGOALS)),)
GO_TEST_PACKAGES := $(filter-out $(GO_TEST_INTEGRATION_PACKAGE) $(GO_TEST_INTEGRATION_PACKAGE)/%,$(shell go list ./...))
ifeq ($(strip $(GO_TEST_PACKAGES)),)
$(error make test could not select any packages. Check go list ./... output and the integration package filter.)
endif
endif
GO_TEST_TARGETS := -timeout 20m $(GO_TEST_PACKAGES)

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
	go run $(GO_BUILD_FLAGS) $(CMD) migrate --execute

# Seed the database with initial user/org/workspace/token.
.PHONY: seed
seed:
	go run $(GO_BUILD_FLAGS) $(CMD) seed

# Tests run inside the test runner image (docker-compose.test.yml), which
# mounts the Docker socket and the source tree at the tree's host path.
# The runner and daemon use the same path for each source file in a bind mount.
# A test that needs FoundationDB, the ledger, or the object store starts it
# through internal/testenv. Each test binary has its own engine containers.
# TestMain and the SIGINT and SIGTERM handler remove that binary's engines.
# A later test binary's first engine start or `make test-env-down` removes
# engines left after SIGKILL or a test timeout panic. Only `go test -short`
# skips tests that need these stores.
TEST_RUNNER := TACK_TEST_ROOT=$(CURDIR) docker compose -f docker-compose.test.yml --profile runner

# The ops, postgres adapter, and audit packages. The audit package carries the
# export and verify scale tests, the gate on the compliance bundle's memory
# footprint, and a footprint assertion nothing runs is not a gate.
.PHONY: test-unit
test-unit:
	$(TEST_RUNNER) build tests
	$(TEST_RUNNER) run --rm tests \
	    test -count=1 -timeout 30m ./internal/ops/... ./internal/adapters/postgres/... ./internal/audit/... ./internal/service/...

# Every package whose tests reach FoundationDB or the ledger, and the go test
# arguments that run them. test-store-host runs them on the current host,
# which needs the FoundationDB client library and a reachable Docker daemon;
# the CI integration job runs it. test-integration runs them in the runner.
TEST_STORE_PACKAGES := ./internal/test/integration/... ./internal/adapters/foundationdb/... \
	./internal/audit/... ./internal/ops/... ./internal/datagen/... ./cmd/server/... ./internal/testenv/...
# The integration tag selects the audit partition tests, which need the ledger.
TEST_STORE_ARGS := -tags integration -count=1 -timeout 30m -v $(TEST_STORE_PACKAGES)

.PHONY: test-store-host
test-store-host:
	go test $(TEST_STORE_ARGS)

.PHONY: test-integration
test-integration:
	$(TEST_RUNNER) build tests
	$(TEST_RUNNER) run --rm tests \
	    test $(TEST_STORE_ARGS)

# These targets run the long runtime audit suites with the runtimeaudit tag.
# test-runtime-audit-host runs them on the current host, which needs the
# FoundationDB client library and a reachable Docker daemon; CI runs this
# target. test-runtime-audit runs them in the test runner container.
TEST_RUNTIME_AUDIT_ARGS := -tags integration,runtimeaudit -count=1 -timeout 40m -v \
	-run '^TestRuntimeAudit(Coverage|TopLevelCoverage|Scaling|MixedVersionRollout)$$' ./internal/test/integration/...

.PHONY: test-runtime-audit-host
test-runtime-audit-host:
	go test $(TEST_RUNTIME_AUDIT_ARGS)

.PHONY: test-runtime-audit
test-runtime-audit:
	$(TEST_RUNNER) build tests
	$(TEST_RUNNER) run --rm tests \
	    test $(TEST_RUNTIME_AUDIT_ARGS)

# The security files must come from Configs with synthetic credentials.
.PHONY: test-service-secrets
test-service-secrets:
	$(if $(TACK_TEST_SEARCH_SECURITY_DIRECTORY),,$(error TACK_TEST_SEARCH_SECURITY_DIRECTORY must identify rendered synthetic Configs security files))
	$(TEST_RUNNER) build tests
	$(TEST_RUNNER) run --rm \
	    --volume "$(TACK_TEST_SEARCH_SECURITY_DIRECTORY):/search-security-fixture:ro" \
	    --env TACK_TEST_SEARCH_SECURITY_DIRECTORY=/search-security-fixture \
	    --env TACK_SEARCH_INTEGRATION=1 tests \
	    test -tags integration -count=1 -timeout 30m -v -run '^TestServiceSecret' ./internal/test/integration/...

# These targets run the OpenSearch-backed search tests. The default engine
# takes 8 GiB and downloads the 555 MB model. test-store-host skips these
# tests. test-search-host runs them on the current host, and test-search runs
# them in the runner. The CI search jobs run test-search-host-group.
TEST_SEARCH_ARGS := -count=1 -timeout 90m -v -run '^TestSearch' ./internal/test/integration/...

.PHONY: test-search-host
test-search-host:
	TACK_TEST_SOURCE_REVISION="$(shell git rev-parse HEAD)" TACK_SEARCH_INTEGRATION=1 go test $(TEST_SEARCH_ARGS)

.PHONY: test-search
test-search:
	$(TEST_RUNNER) build tests
	TACK_TEST_SOURCE_REVISION="$(shell git rev-parse HEAD)" TACK_SEARCH_INTEGRATION=1 $(TEST_RUNNER) run --rm tests \
	    test $(TEST_SEARCH_ARGS)

# Final validation runs the search suite as fixed groups (Step 9, G1 to G6) and
# the repeated tail as three groups (Step 10, T1 to T3). Each G limit is the
# slowest complete CI measurement times 1.5, rounded up to 5 minutes; each T
# limit is three times the slowest single CI run times 1.5. G5 selects every
# search test that G1 to G4 and G6 do not select; a new search test runs in G5.
# G4 and G6 need TACK_SEARCH_CLUSTER=1. CI runs G1, G3, and G5; G2, G4, and G6
# run on the Mac runner.
SEARCH_SKIP_G5 := ^(TestSearchContinuationTraversesDuplicateHeavyCorpus|TestSearchActualOSProcessThroughput|TestSearchSplit.*|TestSearchRestore.*|TestSearchRebuild.*|TestSearchCluster.*)$$
SEARCH_T2_TESTS := TestSearchRebuildExcludesOrphanNodes|TestSearchRebuildStepFitsLease|TestSearchRebuildFailsAfterPauseLimit|TestSearchRebuildDuringChanges|TestSearchRestoreRejectsCursors|TestSearchSplitDuringChanges|TestSearchSplitWithoutModel
SEARCH_T3_TESTS := TestSearchAccessDependentsSurviveEdit|TestSearchAccessUpdatesOlderActiveRevision|TestSearchAccessVersionWithoutRebuild|TestSearchAccessMembershipQueryOnly|TestSearchAccessResourceRefresh|TestSearchAccessRolloutRecovery|TestSearchAccessOnlyUpdateWithoutModel|TestSearchCursorAcrossProcesses|TestSearchCursorExactSortValues|TestSearchCursorOnePredictionPerSession|TestSearchCursorReplayAndConcurrency|TestSearchAccessChangeKeepsPendingContent|TestSearchCursorActualOSProcesses|TestSearchFiltersForbiddenPagesBeforeRanking|TestSearchFinalCheckRejectsCorruptIndexedAccess|TestSearchRevokedAccessRejectsLaterResultsAndReplay|TestSearchDelayedWriter|TestSearchRuntimeIndexesThroughGraph
SEARCH_G1_ARGS := -count=1 -timeout 60m -run '^TestSearchContinuationTraversesDuplicateHeavyCorpus$$'
SEARCH_G2_ARGS := -count=1 -timeout 30m -run '^TestSearchActualOSProcessThroughput$$'
SEARCH_G3_ARGS := -count=1 -timeout 30m -run '^TestSearch(Split|Restore|Rebuild)'
SEARCH_G4_ARGS := -count=1 -timeout 25m -run '^TestSearchCluster' -skip '^TestSearchClusterModelRepair'
SEARCH_G5_ARGS := -count=1 -timeout 95m -tags runtimeaudit -run '^TestSearch' -skip '$(SEARCH_SKIP_G5)'
SEARCH_G6_ARGS := -count=1 -timeout 5m -run '^TestSearchClusterModelRepair'
SEARCH_T1_ARGS := -count=3 -timeout 180m -run '^TestSearchContinuationTraversesDuplicateHeavyCorpus$$'
SEARCH_T2_ARGS := -count=3 -timeout 85m -run '^($(SEARCH_T2_TESTS))$$'
SEARCH_T3_ARGS := -count=3 -timeout 50m -run '^($(SEARCH_T3_TESTS))$$'
SEARCH_GROUP_ARGS = $(or $(SEARCH_$(GROUP)_ARGS),$(error GROUP must be one of G1 G2 G3 G4 G5 G6 T1 T2 T3))

.PHONY: test-search-host-group
test-search-host-group:
	TACK_TEST_SOURCE_REVISION="$(shell git rev-parse HEAD)" TACK_SEARCH_INTEGRATION=1 go test -v $(SEARCH_GROUP_ARGS) ./internal/test/integration/...

.PHONY: test-search-group
test-search-group:
	$(TEST_RUNNER) build tests
	TACK_TEST_SOURCE_REVISION="$(shell git rev-parse HEAD)" TACK_SEARCH_INTEGRATION=1 $(TEST_RUNNER) run --rm tests \
	    test -v $(SEARCH_GROUP_ARGS) ./internal/test/integration/...

# Remove the engines cmd/testenv started and the containers and networks of
# test processes that have exited, then the shared network if containers do
# not use it. Keep engines of running test processes and engines without
# owner labels.
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
