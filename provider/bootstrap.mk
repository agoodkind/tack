# bootstrap.mk obtains the go-makefile bootstrap helper and includes the engine.
# Consumer Makefiles set BINARY, CMD, VPKG, MODULES, and other identity variables
# before including this file. agoodkind/go-makefile maintains the canonical copy.
# Consumers download the helper to receive validation, reuse, and failure updates.

GO_MK_DEV_DIR  ?=
GO_MK_MODULES  ?=
GO_MK          := .make/go.mk
GO_MK_BASE_URL ?= https://raw.githubusercontent.com/agoodkind/go-makefile/main
GO_MK_API_REPO ?= agoodkind/go-makefile
GO_MK_API_REF  ?= main

GO_MK_BOOTSTRAP := .make/scripts/go-mk-bootstrap.sh
# Tests override GO_MK_BOOTSTRAP_BASE_URL with a local server; consumers never
# set it. The helper URL uses GO_MK_API_REF rather than GO_MK_BASE_URL, which
# ends in /main and would ignore a pinned ref.
GO_MK_BOOTSTRAP_BASE_URL ?= https://raw.githubusercontent.com
GO_MK_BOOTSTRAP_URL := $(GO_MK_BOOTSTRAP_BASE_URL)/$(GO_MK_API_REPO)/$(GO_MK_API_REF)/scripts/go-mk-bootstrap.sh

# Preserve an existing helper for offline use. A cold offline start fails.
# Changes to this committed fetch rule require a consumer update.
# --speed-limit and --speed-time stop stalled transfers after about 3 seconds.
# --retry-max-time bounds retries; curl retries speed-limit and max-time errors.
# The 5-second connection timeout allows DNS, TCP, and TLS setup before retrying.
define _go_mk_get_bootstrap
	if [ -n "$(GO_MK_DEV_DIR)" ] && [ -f "$(GO_MK_DEV_DIR)/scripts/go-mk-bootstrap.sh" ]; then \
		mkdir -p .make/scripts; \
		devtmp=$$(mktemp "$(GO_MK_BOOTSTRAP).tmp.XXXXXX") || exit 1; \
		if cp "$(GO_MK_DEV_DIR)/scripts/go-mk-bootstrap.sh" "$$devtmp" && mv "$$devtmp" "$(GO_MK_BOOTSTRAP)"; then \
			: ; \
		else \
			rm -f "$$devtmp"; \
			printf '%s\n' "error: could not install $(GO_MK_BOOTSTRAP) from GO_MK_DEV_DIR=$(GO_MK_DEV_DIR)" >&2; \
			exit 1; \
		fi; \
	elif [ -s "$(GO_MK_BOOTSTRAP)" ]; then \
		: ; \
	else \
		mkdir -p .make/scripts; \
		tmp=$$(mktemp "$(GO_MK_BOOTSTRAP).tmp.XXXXXX") || exit 1; \
		if curl -fsSL --connect-timeout 5 --max-time 15 \
			--speed-limit 1024 --speed-time 3 \
			--retry 3 --retry-delay 2 --retry-max-time 4 \
			"$(GO_MK_BOOTSTRAP_URL)" -o "$$tmp" 2>/dev/null && [ -s "$$tmp" ]; then \
			mv "$$tmp" "$(GO_MK_BOOTSTRAP)"; \
		else \
			rm -f "$$tmp"; \
			printf '%s\n' "error: could not obtain $(GO_MK_BOOTSTRAP). Set GO_MK_DEV_DIR, or check network access to $(GO_MK_BOOTSTRAP_BASE_URL)" >&2; \
			exit 1; \
		fi; \
	fi; \
	chmod +x "$(GO_MK_BOOTSTRAP)"
endef

GO_MK_BOOTSTRAP_FETCHED := 1

ifeq ($(strip $(GO_MK_SKIP_FETCH)),1)
# Reject empty helpers because bash treats an empty script as successful.
# Interrupted downloads, full disks, and placeholders can leave empty files.
# Use the fetch rule's -s check rather than wildcard's existence check.
GO_MK_BOOTSTRAP_PRESENT := $(shell test -s "$(GO_MK_BOOTSTRAP)" && printf yes)
$(if $(GO_MK_BOOTSTRAP_PRESENT),,$(error go-makefile expected a non-empty $(GO_MK_BOOTSTRAP); rerun without GO_MK_SKIP_FETCH))
else
$(shell { $(call _go_mk_get_bootstrap); } 1>&2)
endif

# The helper installs and validates every asset. Stop on failure before parsing
# an incomplete engine. Export every helper input explicitly, including values
# from Makefile assignments and command-line arguments.
# Missing exports can overwrite local assets, ignore GO_MK_SKIP_FETCH, contact
# codeload instead of a test server, or fetch the wrong repository or ref.
# The helper does not read GO_MK_BASE_URL or GO_MK_BOOTSTRAP_BASE_URL.
# go.mk reads the former; this file uses the latter before running the helper.
# Add each new helper input to this invocation.
GO_MK_PROVISION := $(shell GO_MK_API_REPO="$(GO_MK_API_REPO)" GO_MK_API_REF="$(GO_MK_API_REF)" GO_MK_MODULES="$(GO_MK_MODULES)" GO_MK_CODELOAD_BASE="$(GO_MK_CODELOAD_BASE)" GO_MK_DEV_DIR="$(GO_MK_DEV_DIR)" GO_MK_SKIP_FETCH="$(GO_MK_SKIP_FETCH)" bash "$(GO_MK_BOOTSTRAP)" >&2 && printf ok)
$(if $(filter ok,$(GO_MK_PROVISION)),,$(error go-makefile failed to provision its assets))

# go.mk includes modules after defining its variables. Do not include them
# again here; duplicate includes produce overriding-commands warnings.
-include $(GO_MK)
