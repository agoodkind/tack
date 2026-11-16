package servercli

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
	clispec.InputMarker

	Org       string `json:"org"`
	Oldest    string `json:"oldest"`
	Latest    string `json:"latest"`
	Action    string `json:"action"`
	ActorID   string `json:"actor_id"`
	EntityID  string `json:"entity_id"`
	RequestID string `json:"request_id"`
	TraceID   string `json:"trace_id"`
	Limit     int    `json:"limit"`
	Cursor    string `json:"cursor,omitempty"`
}

// auditQueryOp declares `audit query`: the ledger rows of one org over a
// bounded window, read through the audit_reader role and recorded as
// audit.read.
func auditQueryOp(factory *cli.Factory) clispec.Operation[auditQueryInput] {
	return clispec.Operation[auditQueryInput]{
		DryRun: nil,

		Name:     clispec.Name{Canonical: "query", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    auditQuerySpec(),
		Group:    auditGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Print one page of audit.events rows for an org over a bounded RFC3339 window as JSON",
		Long: "The command returns the most recent rows first, up to --limit (capped at 1000) per page. " +
			"A full page includes next_cursor. Pass it as --cursor for the next page.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[auditQueryInput]{
			clispec.StringParam(
				"org",
				"org_id (UUID)",
				"",
				true,
				func(in *auditQueryInput, v string) { in.Org = v },
			),
			clispec.StringParam(
				"oldest",
				"RFC3339 lower bound (inclusive)",
				"",
				true,
				func(in *auditQueryInput, v string) { in.Oldest = v },
			),
			clispec.StringParam(
				"latest",
				"RFC3339 upper bound (exclusive)",
				"",
				true,
				func(in *auditQueryInput, v string) { in.Latest = v },
			),
			clispec.StringParam(
				"action",
				"exact action (verb) match",
				"",
				false,
				func(in *auditQueryInput, v string) { in.Action = v },
			),
			clispec.StringParam(
				"actor-id",
				"actor_id (UUID)",
				"",
				false,
				func(in *auditQueryInput, v string) { in.ActorID = v },
			),
			clispec.StringParam(
				"entity-id",
				"entity_id (UUID)",
				"",
				false,
				func(in *auditQueryInput, v string) { in.EntityID = v },
			),
			clispec.StringParam(
				"request-id",
				"request_id stored in audit context",
				"",
				false,
				func(in *auditQueryInput, v string) { in.RequestID = v },
			),
			clispec.StringParam(
				"trace-id",
				"trace_id stored in audit context",
				"",
				false,
				func(in *auditQueryInput, v string) { in.TraceID = v },
			),
			clispec.IntParam(
				"limit",
				"max rows per page, most recent first, capped at 1000",
				audit.DefaultQueryPageLimit,
				func(in *auditQueryInput, v int) { in.Limit = v },
			),
			clispec.StringParam(
				"cursor",
				"next_cursor from the previous page; empty starts at the newest row",
				"",
				false,
				func(in *auditQueryInput, v string) { in.Cursor = v },
			),
		},
		New: newAuditQueryInput,
		Run: runAuditQuery(factory),
	}
}

func runAuditQuery(
	factory *cli.Factory,
) func(context.Context, auditQueryInput, clispec.ResultSink) error {
	return func(ctx context.Context, input auditQueryInput, sink clispec.ResultSink) error {
		orgID, err := parseAuditUUID(ctx, "audit query", "--org", input.Org)
		if err != nil {
			return err
		}

		oldest, latest, err := parseAuditWindow(ctx, "audit query", input.Oldest, input.Latest)
		if err != nil {
			return err
		}

		actorID, err := parseOptionalAuditUUID(ctx, "audit query", "--actor-id", input.ActorID)
		if err != nil {
			return err
		}

		entityID, err := parseOptionalAuditUUID(ctx, "audit query", "--entity-id", input.EntityID)
		if err != nil {
			return err
		}

		reader, err := openAuditReader(ctx, factory, "audit query")
		if err != nil {
			return err
		}
		defer reader.Close()
		page, err := reader.QueryPage(
			ctx,
			audit.QueryFilter{
				BeforeTime: time.Time{},
				BeforeSeq:  0,
				NilOrg:     false,

				OrgID:     orgID,
				Oldest:    oldest,
				Latest:    latest,
				Action:    input.Action,
				ActorID:   actorID,
				EntityID:  entityID,
				RequestID: input.RequestID,
				TraceID:   input.TraceID,
				Limit:     input.Limit,
			},
			input.Cursor,
		)
		if err != nil {
			slog.ErrorContext(ctx, "audit.query_failed", slog.String("err", err.Error()))

			return fmt.Errorf("audit query: %w", err)
		}

		if page.Rows == nil {
			page.Rows = []audit.Row{}
		}

		return clispec.WriteJSONValue(
			ctx,
			sink,
			auditQueryResult{
				ResultMarker: clispec.ResultMarker{},

				Command:    "audit.query",
				OrgID:      orgID,
				Oldest:     oldest,
				Latest:     latest,
				RowCount:   len(page.Rows),
				Rows:       page.Rows,
				NextCursor: page.NextCursor,
			},
		)
	}
}

type auditGetInput struct {
	clispec.InputMarker

	EventID string `json:"event_id"`
}

// auditGetOp declares `audit get`: one ledger row by event id, read through
// the audit_reader role and recorded as audit.read.
func auditGetOp(factory *cli.Factory) clispec.Operation[auditGetInput] {
	return clispec.Operation[auditGetInput]{
		DryRun: nil,

		Name:     clispec.Name{Canonical: "get", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    auditGetSpec(),
		Group:    auditGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Print one audit.events row by event_id as JSON",
		Long:     "",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[auditGetInput]{
			clispec.StringParam(
				"event-id",
				"event_id (UUID)",
				"",
				true,
				func(in *auditGetInput, v string) { in.EventID = v },
			),
		},
		New: func() auditGetInput {
			return auditGetInput{
				InputMarker: clispec.InputMarker{},
				EventID:     "",
			}
		},
		Run: func(ctx context.Context, in auditGetInput, sink clispec.ResultSink) error {
			eventID, err := parseAuditUUID(ctx, "audit get", "--event-id", in.EventID)
			if err != nil {
				return err
			}

			reader, err := openAuditReader(ctx, factory, "audit get")
			if err != nil {
				return err
			}
			defer reader.Close()
			row, err := reader.GetByID(ctx, eventID)
			if errors.Is(err, pgx.ErrNoRows) {
				return auditEventMissingError{EventID: eventID}
			}

			if err != nil {
				slog.ErrorContext(
					ctx,
					"audit.get_failed",
					slog.String("event_id", eventID.String()),
					slog.String("err", err.Error()),
				)

				return fmt.Errorf("audit get %s: %w", eventID, err)
			}

			return clispec.WriteJSONValue(
				ctx,
				sink,
				auditGetResult{
					ResultMarker: clispec.ResultMarker{},
					Command:      "audit.get",
					Row:          *row,
				},
			)
		},
	}
}

// openAuditReader opens the audit_reader pool from AUDIT_READER_DSN.
func openAuditReader(ctx context.Context, f *cli.Factory, command string) (*audit.Reader, error) {
	reader, err := audit.NewReader(ctx, f.Cfg.AuditReaderDSN)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.reader_open_failed",
			slog.String("command", command),
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("%s: reader from AUDIT_READER_DSN: %w", command, err)
	}

	return reader, nil
}

func parseAuditUUID(ctx context.Context, command, flag, value string) (uuid.UUID, error) {
	nodeID, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.bad_uuid_flag",
			slog.String("command", command),
			slog.String("flag", flag),
			slog.String("err", err.Error()),
		)

		return uuid.Nil, fmt.Errorf("%s: %s must be a UUID: %w", command, flag, err)
	}

	return nodeID, nil
}

func parseOptionalAuditUUID(ctx context.Context, command, flag, value string) (uuid.UUID, error) {
	if strings.TrimSpace(value) == "" {
		return uuid.Nil, nil
	}

	return parseAuditUUID(ctx, command, flag, value)
}

func parseAuditWindow(
	ctx context.Context,
	command string,
	oldestValue string,
	latestValue string,
) (time.Time, time.Time, error) {
	oldest, err := time.Parse(time.RFC3339, oldestValue)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.bad_window_flag",
			slog.String("command", command),
			slog.String("flag", "--oldest"),
			slog.String("err", err.Error()),
		)

		return time.Time{}, time.Time{}, fmt.Errorf(
			"%s: --oldest must be RFC3339: %w",
			command,
			err,
		)
	}

	latest, err := time.Parse(time.RFC3339, latestValue)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.bad_window_flag",
			slog.String("command", command),
			slog.String("flag", "--latest"),
			slog.String("err", err.Error()),
		)

		return time.Time{}, time.Time{}, fmt.Errorf(
			"%s: --latest must be RFC3339: %w",
			command,
			err,
		)
	}

	if !latest.After(oldest) {
		slog.ErrorContext(
			ctx,
			"audit.bad_window_flag",
			slog.String("command", command),
			slog.String("flag", "--latest"),
			slog.String("err", "latest is not after oldest"),
		)

		return time.Time{}, time.Time{}, auditTimeRangeInvalidError{Command: command}
	}

	return oldest, latest, nil
}

type auditEventMissingError struct {
	EventID uuid.UUID
}

func (failure auditEventMissingError) Error() string {
	return fmt.Sprintf("audit get: event %s does not exist", failure.EventID)
}

type auditTimeRangeInvalidError struct {
	Command string
}

func (failure auditTimeRangeInvalidError) Error() string {
	return failure.Command + ": --latest must be after --oldest"
}

func newAuditQueryInput() auditQueryInput {
	return auditQueryInput{
		InputMarker: clispec.InputMarker{},
		Org:         "",
		Oldest:      "",
		Latest:      "",
		Action:      "",
		ActorID:     "",
		EntityID:    "",
		RequestID:   "",
		TraceID:     "",
		Limit:       audit.DefaultQueryPageLimit,
		Cursor:      "",
	}
}
