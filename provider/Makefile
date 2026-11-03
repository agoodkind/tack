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
# both binaries in a Go workspace. The workspace file lives in .make and only the
# release recipe reads it through GOWORK. The helper targets below run with
# GOWORK=off. Every other goal, and any go.work file that a developer keeps in
# this directory, stays unchanged. The tag, package, and publish stages compile
# nothing and need no workspace.
ifneq ($(filter release,$(MAKECMDGOALS)),)
ifeq ($(filter tag package publish,$(RELEASE_STAGE)),)
RELEASE_WORKSPACE := $(CURDIR)/.make/release.work
GO_MK_GENERATE    := postgresql-overlay-source
release: export GOWORK := $(RELEASE_WORKSPACE)
go-mk-bin go-mk-workspace postgresql-overlay-source: export GOWORK := off
endif
endif

include bootstrap.mk

.PHONY: postgresql-overlay-source
postgresql-overlay-source:
	$(MAKE) -C ../postgresql-overlay postgresql-source
	mkdir -p .make
	rm -f "$(RELEASE_WORKSPACE)" "$(RELEASE_WORKSPACE).sum"
	GOWORK="$(RELEASE_WORKSPACE)" go work init "$(CURDIR)" "$(abspath ../postgresql-overlay)" "$(abspath ../postgresql-overlay/src)"
