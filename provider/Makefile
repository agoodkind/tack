# This module builds terraform-provider-tack. make build and make release run
# the shared go-makefile pipeline for this module.

BINARY := terraform-provider-tack
CMD    := .
VPKG   := goodkind.io/tack/provider/internal/version

# A release ships the patched PostgreSQL provider from ../postgresql-overlay
# beside the Tack provider, in one release with one tag.
RELEASE_BINS := terraform-provider-tack:. terraform-provider-postgresql:../postgresql-overlay

GO_MK_MODULES := go-build.mk go-release.mk

# make release fetches and patches the overlay source and builds both binaries in
# a Go workspace. Every other goal (build, test, lint) leaves the overlay source
# out, and a standalone build of this module needs no network access to it.
ifneq ($(filter release,$(MAKECMDGOALS)),)
GO_MK_GENERATE      := postgresql-overlay-source
GO_MK_WORKSPACE_USE := . ../postgresql-overlay ../postgresql-overlay/src
endif

include bootstrap.mk

.PHONY: postgresql-overlay-source
postgresql-overlay-source:
	$(MAKE) -C ../postgresql-overlay postgresql-source
