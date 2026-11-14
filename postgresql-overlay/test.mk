# test-postgresql-overlay runs the patched PostgreSQL provider from
# postgresql-overlay through OpenTofu against a local PostgreSQL server and the
# local YugabyteDB engine that internal/testenv starts. The Compose service
# pgoverlay-tests builds an image with the provider and the pinned OpenTofu for
# the runner architecture. The tests run in that service and run OpenTofu in
# the same container through the Docker SDK. macOS needs
# this route because only a container can connect to the engines' bridge
# addresses. CI runs the same target.
PG_OVERLAY_TEST_ARGS := -tags pgoverlay -count=1 -timeout 30m -v ./internal/test/pgoverlay/...

# Each run uses its own Compose project named after the make process. The EXIT
# trap runs after a pass, a failure, or an interrupt. It stops the run container
# with down, removes the test engines that have the project and service labels
# of this run, then runs down again to remove the network and image. Compose
# does not treat the engines as orphans, and the trap removes them by label.
# Cleanup does not affect other Compose projects. The recipe exits with the test
# status, or with 1 when the tests passed and a cleanup step failed.
PG_OVERLAY_COMPOSE = TACK_TEST_ROOT=$(CURDIR) docker compose -p "$$project" -f docker-compose.test.yml --profile pgoverlay-runner

# The Go caches are external volumes that every run shares and no run removes.
# The names must equal the volume names in docker-compose.test.yml. Creating an
# existing volume leaves it unchanged.
PG_OVERLAY_CACHE_VOLUMES := tack-pgoverlay-go-cache tack-pgoverlay-go-mod

.PHONY: test-postgresql-overlay
test-postgresql-overlay:
	for volume in $(PG_OVERLAY_CACHE_VOLUMES); do docker volume create "$$volume" > /dev/null || exit 1; done; \
	project="tack-pgoverlay-$$$$"; \
	status=0; \
	fail() { \
	    echo "$$1" >&2; \
	    if [ "$$status" -eq 0 ]; then status=1; fi; \
	}; \
	cleanup() { \
	    trap - EXIT INT TERM; \
	    $(PG_OVERLAY_COMPOSE) down --remove-orphans || fail "compose down failed for project $$project"; \
	    leftovers=$$(docker ps -aq --filter "label=com.docker.compose.project=$$project" --filter "label=com.docker.compose.service=testenv-engine"); \
	    if [ -n "$$leftovers" ]; then \
	        docker rm -f -v $$leftovers > /dev/null || fail "removing the test engines of project $$project failed"; \
	    fi; \
	    $(PG_OVERLAY_COMPOSE) down --remove-orphans --rmi local || fail "compose down failed for project $$project"; \
	    exit "$$status"; \
	}; \
	trap cleanup EXIT; \
	trap 'status=130; exit 130' INT TERM; \
	{ $(PG_OVERLAY_COMPOSE) build pgoverlay-tests && \
	  $(PG_OVERLAY_COMPOSE) run --rm pgoverlay-tests test $(PG_OVERLAY_TEST_ARGS); } || status=$$?; \
	exit "$$status"
