# go-makefile

Shared Go build targets and reusable GitHub Actions workflows for all `agoodkind` Go projects.

## What's in here

| File | Purpose |
|------|---------|
| `go.mk` | Shared Makefile targets: `lint`, `fmt`, `vet`, `test`, `govulncheck`, `check` |
| `.github/workflows/_ci.yml` | Reusable CI workflow (4 independent required-check jobs) |
| `.github/workflows/_release.yml` | Reusable release workflow (timestamp tag + goreleaser) |
| `goreleaser-template.yaml` | Starter `.goreleaser.yaml` to copy into a new project |
| `golangci-template.yml` | Starter `.golangci.yml` with preferred lint rules |

---

## Quickstart: adopting in a new Go project

### 1. Bootstrap your Makefile

`go.mk` is fetched at runtime — nothing is committed. GNU Make will auto-download it the first time any target is run, then re-read the Makefile with the shared targets available.

Add this to the top of your project `Makefile`:

```makefile
GO_MK_URL   := https://raw.githubusercontent.com/agoodkind/go-makefile/main/go.mk
GO_MK       := .make/go.mk
GO_MK_CACHE := $(HOME)/.cache/go-makefile/go.mk

# Auto-download go.mk if missing. On success, update the local cache.
# On failure, fall back to the last known good cache. If neither exists, fail.
# GNU Make re-reads after building an included file, so any target works
# on a fresh clone without a separate bootstrap step.
$(GO_MK):
	@mkdir -p $(dir $@)
	@if curl -fsSL --connect-timeout 5 --max-time 10 "$(GO_MK_URL)" -o "$@"; then \
		mkdir -p "$(dir $(GO_MK_CACHE))" && cp "$@" "$(GO_MK_CACHE)"; \
	elif [ -f "$(GO_MK_CACHE)" ]; then \
		echo "warning: go.mk fetch failed, using cached version" >&2; \
		cp "$(GO_MK_CACHE)" "$@"; \
	else \
		echo "error: go.mk fetch failed and no cache available" >&2; \
		exit 1; \
	fi

-include $(GO_MK)

# Explicitly pull the latest go.mk and update the cache.
.PHONY: sync
sync:
	@mkdir -p "$(dir $(GO_MK))"
	@if curl -fsSL --connect-timeout 5 --max-time 10 "$(GO_MK_URL)" -o "$(GO_MK)"; then \
		mkdir -p "$(dir $(GO_MK_CACHE))" && cp "$(GO_MK)" "$(GO_MK_CACHE)"; \
		echo "go.mk updated"; \
	else \
		echo "error: go.mk fetch failed" >&2; \
		exit 1; \
	fi
```

Then add your project-specific targets below. Example:

```makefile
BINARY := your-project
CMD    := ./cmd/$(BINARY)

.DEFAULT_GOAL := build

.PHONY: build deploy clean

build:
	go build $(CMD)

deploy:
	go install $(CMD)

clean:
	rm -f $(BINARY)
```

### 2. Ignore the fetched file

Add to `.gitignore`:

```
.make/
```

### 3. Wire up CI

Create `.github/workflows/ci.yml`:

```yaml
name: CI

on: [push]

jobs:
  ci:
    uses: agoodkind/go-makefile/.github/workflows/_ci.yml@main
```

This gives you four independent required status checks: `Build and Test`, `Vet`, `Govulncheck`, `GoReleaser Config Check`.

The CI workflow fetches dependencies directly — no `make sync` step needed.

### 4. Wire up releases

Create `.github/workflows/release.yml`:

```yaml
name: Release

on:
  push:
    branches: [main]

concurrency:
  group: release
  cancel-in-progress: true

jobs:
  release:
    uses: agoodkind/go-makefile/.github/workflows/_release.yml@main
    permissions:
      contents: write
    secrets: inherit
```

Every push to `main` creates a release tagged `YYYYMMDDHHmm-<hex-build>-<short-sha>` (e.g. `202604101430-f-a1b2c3d`). A newer push cancels any in-progress release run.

### 5. Copy the goreleaser template

```sh
cp <(curl -fsSL https://raw.githubusercontent.com/agoodkind/go-makefile/main/goreleaser-template.yaml) .goreleaser.yaml
```

Edit `.goreleaser.yaml` and set `project_name` and `builds.main`/`builds.binary`.

### 6. Copy the golangci template

```sh
cp <(curl -fsSL https://raw.githubusercontent.com/agoodkind/go-makefile/main/golangci-template.yml) .golangci.yml
```

---

## How updates work

### `go.mk` (local make targets)

Always fetched fresh. Running `make sync` pulls the latest version immediately. Running any make target on a fresh clone auto-bootstraps via curl. No subtree pull, no manual step.

### GitHub Actions workflows

The CI and release workflows call `_ci.yml@main` and `_release.yml@main` — they always run the latest version of the shared workflow on every push. No consumer action needed.

### Templates (goreleaser, golangci)

One-time copy. Update manually when needed by re-running the `cp` command above.
