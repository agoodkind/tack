package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type auditQueryInput struct {
	clispec.InputMarker `exhaustruct:"optional"`
	Org                 string
	Oldest              string
	Latest              string
	Action              string
	ActorID             string
	EntityID            string
	RequestID           string
	TraceID             string
	Limit               int
	Cursor              string
}

// auditQueryOp declares `audit query`: the ledger rows of one org over a
// bounded window, read through the audit_reader role and recorded as
// audit.read.
func auditQueryOp(f *cli.Factory) clispec.Operation[auditQueryInput] {
	return clispec.Operation[auditQueryInput]{
		Name:    clispec.Name{Canonical: "query", CLIOverride: ""},
		Audit:   audit.Spec{Verb: string(audit.VerbAuditRead), Reads: true},
		Group:   auditGroup,
		Aliases: nil,
		Hidden:  false,
		Short:   "Print one page of audit.events rows for an org over a bounded RFC3339 window as JSON",
		Long: "Rows return most recent first, at most --limit (capped at 1000) per page. " +
			"A full page carries next_cursor; pass it back as --cursor for the next page.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[auditQueryInput]{
			clispec.StringParam("org", "org_id (UUID)", "", true, func(in *auditQueryInput, v string) { in.Org = v }),
			clispec.StringParam("oldest", "RFC3339 lower bound (inclusive)", "", true, func(in *auditQueryInput, v string) { in.Oldest = v }),
			clispec.StringParam("latest", "RFC3339 upper bound (exclusive)", "", true, func(in *auditQueryInput, v string) { in.Latest = v }),
			clispec.StringParam("action", "exact action (verb) match", "", false, func(in *auditQueryInput, v string) { in.Action = v }),
			clispec.StringParam("actor-id", "actor_id (UUID)", "", false, func(in *auditQueryInput, v string) { in.ActorID = v }),
			clispec.StringParam("entity-id", "entity_id (UUID)", "", false, func(in *auditQueryInput, v string) { in.EntityID = v }),
			clispec.StringParam("request-id", "request_id stored in audit context", "", false, func(in *auditQueryInput, v string) { in.RequestID = v }),
			clispec.StringParam("trace-id", "trace_id stored in audit context", "", false, func(in *auditQueryInput, v string) { in.TraceID = v }),
			clispec.IntParam("limit", "max rows per page, most recent first, capped at 1000", audit.DefaultQueryPageLimit, func(in *auditQueryInput, v int) { in.Limit = v }),
			clispec.StringParam("cursor", "next_cursor from the previous page; empty starts at the newest row", "", false, func(in *auditQueryInput, v string) { in.Cursor = v }),
		},
		New: func() auditQueryInput {
			return auditQueryInput{
				InputMarker: clispec.InputMarker{}, Org: "", Oldest: "", Latest: "", Action: "",
				ActorID: "", EntityID: "", RequestID: "", TraceID: "", Limit: audit.DefaultQueryPageLimit,
				Cursor: "",
			}
		},
		Run: runAuditQuery(f),
	}
}

func runAuditQuery(f *cli.Factory) func(context.Context, auditQueryInput, clispec.ResultSink) error {
	return func(ctx context.Context, in auditQueryInput, sink clispec.ResultSink) error {
		orgID, err := parseAuditUUID(ctx, "audit query", "--org", in.Org)
		if err != nil {
			return err
		}
		oldest, latest, err := parseAuditWindow(ctx, "audit query", in.Oldest, in.Latest)
		if err != nil {
			return err
		}
		actorID, err := parseOptionalAuditUUID(ctx, "audit query", "--actor-id", in.ActorID)
		if err != nil {
			return err
		}
		entityID, err := parseOptionalAuditUUID(ctx, "audit query", "--entity-id", in.EntityID)
		if err != nil {
			return err
		}
		reader, err := openAuditReader(ctx, f, "audit query")
		if err != nil {
			return err
		}
		defer reader.Close()
		page, err := reader.QueryPage(ctx, audit.QueryFilter{
			OrgID: orgID, Oldest: oldest, Latest: latest, Action: in.Action,
			ActorID: actorID, EntityID: entityID, RequestID: in.RequestID, TraceID: in.TraceID,
			Limit: in.Limit,
		}, in.Cursor)
		if err != nil {
			slog.ErrorContext(ctx, "audit.query_failed", slog.String("err", err.Error()))
			return fmt.Errorf("audit query: %w", err)
		}
		if page.Rows == nil {
			page.Rows = []audit.Row{}
		}
		return clispec.WriteJSONValue(ctx, sink, auditQueryResult{
			Command: "audit.query", OrgID: orgID, Oldest: oldest, Latest: latest,
			RowCount: len(page.Rows), Rows: page.Rows, NextCursor: page.NextCursor,
		})
	}
}

type auditGetInput struct {
	clispec.InputMarker `exhaustruct:"optional"`
	EventID             string
}

// auditGetOp declares `audit get`: one ledger row by event id, read through
// the audit_reader role and recorded as audit.read.
func auditGetOp(f *cli.Factory) clispec.Operation[auditGetInput] {
	return clispec.Operation[auditGetInput]{
		Name:     clispec.Name{Canonical: "get", CLIOverride: ""},
		Audit:    audit.Spec{Verb: string(audit.VerbAuditRead), Reads: true},
		Group:    auditGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Print one audit.events row by event_id as JSON",
		Long:     "",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[auditGetInput]{
			clispec.StringParam("event-id", "event_id (UUID)", "", true, func(in *auditGetInput, v string) { in.EventID = v }),
		},
		New: func() auditGetInput { return auditGetInput{InputMarker: clispec.InputMarker{}, EventID: ""} },
		Run: func(ctx context.Context, in auditGetInput, sink clispec.ResultSink) error {
			eventID, err := parseAuditUUID(ctx, "audit get", "--event-id", in.EventID)
			if err != nil {
				return err
			}
			reader, err := openAuditReader(ctx, f, "audit get")
			if err != nil {
				return err
			}
			defer reader.Close()
			row, err := reader.GetByID(ctx, eventID)
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("audit get: no event with id %s", eventID)
			}
			if err != nil {
				slog.ErrorContext(ctx, "audit.get_failed", slog.String("event_id", eventID.String()), slog.String("err", err.Error()))
				return fmt.Errorf("audit get %s: %w", eventID, err)
			}
			return clispec.WriteJSONValue(ctx, sink, auditGetResult{Command: "audit.get", Row: *row})
		},
	}
}

// openAuditReader opens the audit_reader pool from AUDIT_READER_DSN.
func openAuditReader(ctx context.Context, f *cli.Factory, command string) (*audit.Reader, error) {
	reader, err := audit.NewReader(ctx, f.Cfg.AuditReaderDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit.reader_open_failed", slog.String("command", command), slog.String("err", err.Error()))
		return nil, fmt.Errorf("%s: reader: %w", command, err)
	}
	return reader, nil
}

func parseAuditUUID(ctx context.Context, command, flag, value string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		slog.ErrorContext(ctx, "audit.bad_uuid_flag", slog.String("command", command), slog.String("flag", flag), slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("%s: %s must be a UUID: %w", command, flag, err)
	}
	return id, nil
}

func parseOptionalAuditUUID(ctx context.Context, command, flag, value string) (uuid.UUID, error) {
	if strings.TrimSpace(value) == "" {
		return uuid.Nil, nil
	}
	return parseAuditUUID(ctx, command, flag, value)
}

func parseAuditWindow(ctx context.Context, command, oldestValue, latestValue string) (time.Time, time.Time, error) {
	oldest, err := time.Parse(time.RFC3339, oldestValue)
	if err != nil {
		slog.ErrorContext(ctx, "audit.bad_window_flag", slog.String("command", command), slog.String("flag", "--oldest"), slog.String("err", err.Error()))
		return time.Time{}, time.Time{}, fmt.Errorf("%s: --oldest must be RFC3339: %w", command, err)
	}
	latest, err := time.Parse(time.RFC3339, latestValue)
	if err != nil {
		slog.ErrorContext(ctx, "audit.bad_window_flag", slog.String("command", command), slog.String("flag", "--latest"), slog.String("err", err.Error()))
		return time.Time{}, time.Time{}, fmt.Errorf("%s: --latest must be RFC3339: %w", command, err)
	}
	if !latest.After(oldest) {
		slog.ErrorContext(ctx, "audit.bad_window_flag", slog.String("command", command), slog.String("flag", "--latest"), slog.String("err", "latest is not after oldest"))
		return time.Time{}, time.Time{}, fmt.Errorf("%s: --latest must be after --oldest", command)
	}
	return oldest, latest, nil
}
