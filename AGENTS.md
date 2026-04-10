# Agent Rules for Tack

These rules apply to every AI agent working in this repository.
They supplement CLAUDE.md (which covers architecture and product decisions).
This file covers code style, structure, and observable behavior requirements.

## No TOML. No Config Files.

Server configuration is environment variables only. There is no config file.
Do not introduce TOML, YAML, JSON, or any file-based config loading.
The `caarlos0/env` library is the correct and only config mechanism for the Go server.

The Python MCP client also uses env vars only. `TACK_URL`, `TACK_TOKEN`, `TACK_LOG_LEVEL`.
The only file path resolved from XDG is the log file location.

## Logging

### Go

Use `log/slog` throughout. The global logger is initialized by `telemetry.Setup`.
Inside request handlers and services, retrieve the context logger via `telemetry.L(ctx)`.

Every significant event must be logged. "Significant" means:

- Entity created, updated, deleted, moved (service layer, `issue.created`, `issue.moved`, etc.)
- Background jobs scheduled or completed
- Auth failures
- Startup and shutdown

Use named `slog.Attr` fields, never positional:

```go
// Correct
telemetry.L(ctx).Info("issue.created",
    slog.String("issue_id", created.ID.String()),
    slog.String("project_id", i.ProjectID.String()),
    slog.Int("sequence_id", int(created.SequenceID)),
)

// Wrong
slog.Info("issue created", "id", created.ID)
```

Three levels, used strictly:

- `Info`: normal flow events (entity lifecycle, startup, shutdown, request summary)
- `Debug`: trace-level detail (individual SQL queries via QueryTracer, FDB key writes)
- `Error`: actual failures (auth rejected, repo error, initialization failure)

Log message names use `noun.verb` dot notation: `issue.created`, `hook.blocked`, `worker.started`.

### Python

Use the `logging` module with the `JsonFormatter` from `tack_mcp_client/log.py`.
Every step in the bridge logs: startup, stdin receipt, HTTP request, HTTP response, stdout write, EOF.
Pass context as `extra={}` kwargs so they appear as top-level JSON fields.

```python
# Correct
logger.info("bridge.request", extra={"url": url, "session_id": session_id, "msg_len": len(raw_message)})

# Wrong
logger.info(f"sending to {url}")
```

## XDG Base Directories

XDG applies to file paths (log files), never to configuration values.

For the Python client:
- Log file: `$XDG_STATE_HOME/tack/mcp-client.log` (default `~/.local/state/tack/mcp-client.log`)
- Implemented in `tack_mcp_client/xdg.py`

The Go server's `LOG_FILE` env var can be pointed at any path. The systemd unit and
docker-compose use explicit paths. There is no XDG resolution server-side.

## File Size and Concern Separation

No file should exceed 200 lines. If it does, split it.

Split by concern, not by accident:

- One file per entity for conversion functions: `convert_issue.go`, `convert_project.go`, etc.
- Bulk operations in a separate file from CRUD: `issue_bulk.go`, `issue_handler_bulk.go`
- One file per logical grouping in MCP tools: `issue.go` (CRUD), `issue_bulk.go`, `issue_move.go`

Name files after their responsibility. A file named `utils.go` or `helpers.go` is a last resort,
only for genuinely shared code with no better home.

## Readability Over Conciseness

### Variable names

Use the full domain term, not abbreviations:

```go
// Correct
workspaceID, projectID, issueID

// Wrong
wsID, pID, iID
```

### Error messages

Every error must include context about what failed and the relevant identifier:

```go
// Correct
fmt.Errorf("get issue %s: %w", id, err)
fmt.Errorf("create issue in project %s: %w", i.ProjectID, err)
fmt.Errorf("bulk delete %d issues: %w", len(issueIDs), err)

// Wrong
return nil, err
```

### Package doc comments

Every package must have a `// Package X ...` doc comment explaining:
1. What the package does in one sentence
2. Any design decision that is not obvious from the code

```go
// Package service implements business logic for all Tack entities.
// It coordinates SQL repositories and FoundationDB stores, using errgroup
// for concurrent multi-source reads.
package service
```

### Type and field comments

Every exported type and non-obvious field must have a doc comment:

```go
// BulkUpdatePatch describes a set of changes to apply atomically to multiple issues.
// Fields with nil pointer values are left unchanged. AssigneeIDs replaces the full
// assignee set when non-nil; an empty slice clears all assignees.
type BulkUpdatePatch struct {
    IssueIDs  []uuid.UUID
    ProjectID uuid.UUID
    // StateID replaces the state on all matched issues when non-nil.
    StateID *uuid.UUID
    // SetEpicID must be true to apply an EpicID change (distinguishes nil-to-clear from not-set).
    SetEpicID bool
    EpicID    *uuid.UUID
}
```

## What Not To Do

- Do not add TOML, YAML, or any config file format.
- Do not add error handling for scenarios that provably cannot happen.
- Do not add backwards-compatibility shims, unused exports, or re-exports.
- Do not add docstrings or comments to code you did not change.
- Do not add features not explicitly requested.
- Do not use `utils.go` or `helpers.go` as a dumping ground.
- Do not run `go build ./...` from a directory that is not the module root.
  The correct command is: `bash -c "cd /Users/agoodkind/Sites/tack && /opt/homebrew/bin/go build ./cmd/server/"`
