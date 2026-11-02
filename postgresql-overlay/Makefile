# This module builds terraform-provider-postgresql from a pinned upstream
# revision plus role-state.patch. make build and make release run the shared
# go-makefile pipeline. The generate hook below fetches the pinned revision,
# applies the patch into src, and go-makefile routes the build to src through
# a generated go.work.

BINARY := terraform-provider-postgresql
CMD    := .

# Pinned upstream source. The generate hook fails unless the tag on GitHub, the
# pinned revision, and the fetched commit all name the same revision.
PG_PROVIDER_REPOSITORY := https://github.com/cyrilgdn/terraform-provider-postgresql.git
PG_PROVIDER_VERSION    := v1.27.0
PG_PROVIDER_REVISION   := ff936b9548c1076f498523a2bd116915ff977142
PG_PROVIDER_PATCH      := role-state.patch
PG_PROVIDER_SOURCE     := src

GO_MK_MODULES       := go-build.mk go-release.mk
GO_MK_GENERATE      := postgresql-source
GO_MK_WORKSPACE_USE := . ./$(PG_PROVIDER_SOURCE)

include bootstrap.mk

# This target is phony. Every build deletes src and recreates it from the
# pinned revision. A warm checkout never keeps an edited or stale src.
.PHONY: postgresql-source
postgresql-source:
	rm -rf $(PG_PROVIDER_SOURCE)
	test "$$(git ls-remote --tags $(PG_PROVIDER_REPOSITORY) refs/tags/$(PG_PROVIDER_VERSION) | cut -f1)" = "$(PG_PROVIDER_REVISION)"
	git init -q $(PG_PROVIDER_SOURCE)
	git -C $(PG_PROVIDER_SOURCE) fetch -q --depth 1 $(PG_PROVIDER_REPOSITORY) $(PG_PROVIDER_REVISION)
	git -C $(PG_PROVIDER_SOURCE) checkout -q --detach FETCH_HEAD
	test "$$(git -C $(PG_PROVIDER_SOURCE) rev-parse HEAD)" = "$(PG_PROVIDER_REVISION)"
	git -C $(PG_PROVIDER_SOURCE) apply ../$(PG_PROVIDER_PATCH)
