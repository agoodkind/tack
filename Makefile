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

# Match the source mount to the host path for Docker SDK bind mounts.
# Tests start stores through internal/testenv. TestMain, signal handlers, and
# a watcher remove owned engines. After SIGKILL ends the whole runner, the
# next engine start or test-env-down removes engines of exited processes.
TEST_RUNNER := TACK_TEST_ROOT=$(CURDIR) docker compose -f docker-compose.test.yml --profile runner
TEST_COMPOSE_TARGETS := test-unit test-integration test-runtime-audit test-search

test-unit: TEST_ARGS = -count=1 -timeout 30m ./internal/ops/... ./internal/adapters/postgres/... ./internal/audit/... ./internal/service/...

# Host targets need the FoundationDB client library and a Docker daemon.
TEST_STORE_OTHER_PACKAGES := ./internal/adapters/foundationdb/... ./internal/audit/... \
	./internal/ops/... ./internal/datagen/... ./cmd/server/... ./internal/testenv/...
TEST_STORE_PACKAGES := ./internal/test/integration/... $(TEST_STORE_OTHER_PACKAGES)
# The integration tag includes audit partition and FoundationDB retention tests.
TEST_STORE_FLAGS := -tags integration -count=1 -timeout 30m -v
TEST_STORE_ARGS := $(TEST_STORE_FLAGS) $(TEST_STORE_PACKAGES)

test-store-host test-integration: TEST_ARGS = $(TEST_STORE_ARGS)

# CI partitions integration tests by the first character after Test.
ifeq ($(SHARD),integration-a-through-l)
test-store-host-shard: TEST_ARGS = $(TEST_STORE_FLAGS) -run '^Test[A-L]' ./internal/test/integration/...
else ifeq ($(SHARD),remaining-integration-tests)
test-store-host-shard: TEST_ARGS = $(TEST_STORE_FLAGS) -run '^Test[^A-L]' ./internal/test/integration/...
else ifeq ($(SHARD),other-store-packages)
test-store-host-shard: TEST_ARGS = $(TEST_STORE_FLAGS) $(TEST_STORE_OTHER_PACKAGES)
else
test-store-host-shard: TEST_ARGS = $(error test-store-host-shard requires SHARD=integration-a-through-l, SHARD=remaining-integration-tests, or SHARD=other-store-packages)
endif

TEST_RUNTIME_AUDIT_ARGS := -tags integration,runtimeaudit -count=1 -timeout 40m -v \
	-run '^TestRuntimeAudit(Coverage|CoverageWaitsForTargetEvent|TopLevelCoverage|Scaling|MixedVersionRollout)$$' ./internal/test/integration/...
test-runtime-audit-host test-runtime-audit: TEST_ARGS = $(TEST_RUNTIME_AUDIT_ARGS)

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

# Search opt-in starts an 8 GiB engine and downloads the 555 MB model.
TEST_SEARCH_ARGS := -count=1 -timeout 90m -v -run '^TestSearch' ./internal/test/integration/...
test-search-host test-search: TEST_ARGS = $(TEST_SEARCH_ARGS)
test-search-host test-search test-search-host-group test-search-group: TEST_ENV = TACK_TEST_SOURCE_REVISION="$(shell git rev-parse HEAD)" TACK_SEARCH_INTEGRATION=1

# Dedicated groups exclude their tests from the alphabetical partitions.
# Cluster groups require TACK_SEARCH_CLUSTER=1. The Mac runner runs those
# groups and throughput-with-server-processes; CI runs the other suite groups.
SEARCH_RUNTIME_AND_SESSION_AUDIT_TESTS := ^TestSearch(RuntimeAudit|SessionAudit)
SEARCH_SEPARATE_FEATURE_TESTS := ^(TestSearchContinuationTraversesDuplicateHeavyCorpus|TestSearchActualOSProcessThroughput|TestSearchSplit.*|TestSearchRestore.*|TestSearchRebuild.*|TestSearchCluster.*)$$
SEARCH_REPEAT_REPLACEMENT_TESTS := TestSearchRebuildExcludesOrphanNodes|TestSearchRebuildStepFitsLease|TestSearchRebuildFailsAfterPauseLimit|TestSearchRebuildDuringChanges|TestSearchRestoreRejectsCursors|TestSearchSplitDuringChanges|TestSearchSplitWithoutModel
SEARCH_REPEAT_ACCESS_CURSOR_WORKER_TESTS := TestSearchAccessDependentsSurviveEdit|TestSearchAccessUpdatesOlderActiveRevision|TestSearchAccessVersionWithoutRebuild|TestSearchAccessMembershipQueryOnly|TestSearchAccessResourceRefresh|TestSearchAccessRolloutRecovery|TestSearchAccessOnlyUpdateWithoutModel|TestSearchCursorAcrossProcesses|TestSearchCursorExactSortValues|TestSearchCursorOnePredictionPerSession|TestSearchCursorReplayAndConcurrency|TestSearchAccessChangeKeepsPendingContent|TestSearchCursorActualOSProcesses|TestSearchFiltersForbiddenPagesBeforeRanking|TestSearchFinalCheckRejectsCorruptIndexedAccess|TestSearchRevokedAccessRejectsLaterResultsAndReplay|TestSearchDelayedWriter|TestSearchRuntimeIndexesThroughGraph
SEARCH_continuation-with-duplicate-pages_ARGS := -count=1 -timeout 60m -run '^TestSearchContinuationTraversesDuplicateHeavyCorpus$$'
SEARCH_throughput-with-server-processes_ARGS := -count=1 -timeout 30m -run '^TestSearchActualOSProcessThroughput$$'
SEARCH_rebuild-split-and-restore_ARGS := -count=1 -timeout 45m -run '^TestSearch(Split|Restore|Rebuild)'
SEARCH_cluster-failures-and-recovery_ARGS := -count=1 -timeout 25m -run '^TestSearchCluster' -skip '^TestSearchClusterModelRepair'
SEARCH_features-a-through-m_ARGS := -count=1 -timeout 75m -tags runtimeaudit -run '^TestSearch[A-M]' -skip '$(SEARCH_SEPARATE_FEATURE_TESTS)'
SEARCH_remaining-features_ARGS := -count=1 -timeout 75m -tags runtimeaudit -run '^TestSearch($$|[^A-M])' -skip '$(SEARCH_SEPARATE_FEATURE_TESTS)|$(SEARCH_RUNTIME_AND_SESSION_AUDIT_TESTS)'
SEARCH_runtime-and-session-audit_ARGS := -count=1 -timeout 75m -tags runtimeaudit -run '$(SEARCH_RUNTIME_AND_SESSION_AUDIT_TESTS)'
SEARCH_model-repair-enablement_ARGS := -count=1 -timeout 5m -run '^TestSearchClusterModelRepair'
SEARCH_repeat-continuation-with-duplicate-pages_ARGS := -count=3 -timeout 180m -run '^TestSearchContinuationTraversesDuplicateHeavyCorpus$$'
SEARCH_repeat-rebuild-split-and-restore_ARGS := -count=3 -timeout 85m -run '^($(SEARCH_REPEAT_REPLACEMENT_TESTS))$$'
SEARCH_repeat-access-cursors-and-workers_ARGS := -count=3 -timeout 50m -run '^($(SEARCH_REPEAT_ACCESS_CURSOR_WORKER_TESTS))$$'
SEARCH_GROUP_ARGS = $(or $(SEARCH_$(GROUP)_ARGS),$(error GROUP must be one of continuation-with-duplicate-pages throughput-with-server-processes rebuild-split-and-restore cluster-failures-and-recovery features-a-through-m remaining-features runtime-and-session-audit model-repair-enablement repeat-continuation-with-duplicate-pages repeat-rebuild-split-and-restore repeat-access-cursors-and-workers))
TEST_SEARCH_RUNNER ?= compose

test-search-host-group test-search-group: TEST_ARGS = -v $(SEARCH_GROUP_ARGS) ./internal/test/integration/...

.PHONY: test-search-group
ifeq ($(TEST_SEARCH_RUNNER),mac)
test-search-group:
	$(if $(strip $(MAC_RUNNER_IMAGE)),,$(error set MAC_RUNNER_IMAGE to an existing immutable local image ID))
	$(if $(strip $(MAC_RUNNER_EVIDENCE_DIR)),,$(error set MAC_RUNNER_EVIDENCE_DIR to a new absolute directory outside the checkout))
	@if [ ! -x "$(CURDIR)/dist/testenv" ]; then \
	    printf '%s\n' 'Build dist/testenv before testing: make build BINARY=testenv CMD=./cmd/testenv' >&2; \
	    exit 1; \
	fi
	"$(CURDIR)/dist/testenv" run-mac --root "$(CURDIR)" --image "$(MAC_RUNNER_IMAGE)" \
	    --evidence-dir "$(MAC_RUNNER_EVIDENCE_DIR)" -- -v $(SEARCH_GROUP_ARGS)
else ifeq ($(TEST_SEARCH_RUNNER),compose)
TEST_COMPOSE_TARGETS += test-search-group
else
test-search-group:
	$(error TEST_SEARCH_RUNNER must be compose or mac)
endif

.PHONY: test-store-host test-store-host-shard test-runtime-audit-host test-search-host test-search-host-group
test-store-host test-store-host-shard test-runtime-audit-host test-search-host test-search-host-group:
	$(TEST_ENV) go test $(TEST_ARGS)

.PHONY: $(TEST_COMPOSE_TARGETS)
$(TEST_COMPOSE_TARGETS):
	$(TEST_RUNNER) build tests
	$(TEST_ENV) $(TEST_RUNNER) run --rm tests \
	    test $(TEST_ARGS)

# Remove cmd/testenv engines and resources of exited test processes.
# Preserve engines of running processes and engines without owner labels.
.PHONY: test-env-down
test-env-down:
	go run ./cmd/testenv down

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
