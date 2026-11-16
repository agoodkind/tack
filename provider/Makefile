# This module builds terraform-provider-tack. make build and make release run
# the shared go-makefile pipeline for this module.

BINARY := terraform-provider-tack
CMD    := .
VPKG   := goodkind.io/tack/provider/internal/version

# A release ships the patched PostgreSQL provider from ../postgresql-overlay
# beside the Tack provider, in one release with one tag.
RELEASE_BINS := terraform-provider-tack:. terraform-provider-postgresql:../postgresql-overlay

GO_MK_MODULES := go-build.mk go-release.mk

# The compile stages of make release fetch and patch the overlay source and build
# both binaries in a Go workspace. Only the release recipe reads the .make
# workspace file using GOWORK. The helper targets below run with GOWORK=off.
# Other goals and developer go.work files remain unchanged. The tag, package,
# and publish stages do not compile binaries or require a workspace.
ifneq ($(filter release,$(MAKECMDGOALS)),)
  ifeq ($(filter tag package publish,$(RELEASE_STAGE)),)
    RELEASE_WORKSPACE := $(CURDIR)/.make/release.work
    GO_MK_GENERATE    := postgresql-overlay-source

    release: export GOWORK := $(RELEASE_WORKSPACE)
    go-mk-bin go-mk-workspace postgresql-overlay-source: export GOWORK := off
  endif
endif

include bootstrap.mk

ifeq ($(GITHUB_ACTIONS),true)
  GO_TEST_TARGETS := -short -count=1 -timeout 20m $(GO_TEST_TARGETS)
endif

.PHONY: postgresql-overlay-source
postgresql-overlay-source:
	$(MAKE) -C ../postgresql-overlay postgresql-source
	mkdir -p .make
	rm -f "$(RELEASE_WORKSPACE)" "$(RELEASE_WORKSPACE).sum"
	GOWORK="$(RELEASE_WORKSPACE)" go work init \
	    "$(CURDIR)" \
	    "$(abspath ../postgresql-overlay)" \
	    "$(abspath ../postgresql-overlay/src)"
