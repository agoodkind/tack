# go-makefile owns build and lint. Do not add project-local lint targets.
# Run make help for its entry points and baseline targets.

BINARY := tack
CMD    := ./cmd/server

# CGO required for FoundationDB bindings; -tags fdb gates the FDB code paths.
export CGO_ENABLED := 1
GO_BUILD_TAGS       := fdb

# Pipeline modules (skip go-release.mk: tack ships prebuilt container images
# from CI, not GoReleaser).
GO_MK_MODULES := go-build.mk

# Exclude integration tests from the shared test target.
GO_TEST_INTEGRATION_PACKAGE := goodkind.io/tack/internal/test/integration
ifneq ($(filter test,$(MAKECMDGOALS)),)
  GO_TEST_PACKAGES := $(filter-out \
      $(GO_TEST_INTEGRATION_PACKAGE) $(GO_TEST_INTEGRATION_PACKAGE)/%,\
      $(shell go list ./...))
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

ifeq ($(GITHUB_ACTIONS),true)
  GO_TEST_TARGETS := -short -count=1 $(GO_TEST_TARGETS)
endif

FDB_VERSION ?= 7.4.6

.PHONY: ci-fdb-client
ci-fdb-client:
	gh release download "$(FDB_VERSION)" --repo apple/foundationdb \
	    --pattern "foundationdb-clients_$(FDB_VERSION)-1_amd64.deb" \
	    --output "$(RUNNER_TEMP)/fdb-clients.deb"
	sudo dpkg -i "$(RUNNER_TEMP)/fdb-clients.deb"
