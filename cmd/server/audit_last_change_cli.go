package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

type auditLastChangeInput struct {
	clispec.InputMarker `exhaustruct:"optional"`
	Org                 string
	EntityID            string
	Oldest              string
	Latest              string
	Path                string
	Cursor              string
}

// auditLastChangeResult is the `audit last-change` output for one node.
type auditLastChangeResult struct {
	clispec.ResultMarker `exhaustruct:"optional"`
	Command              string    `json:"command"`
	OrgID                uuid.UUID `json:"org_id"`
	EntityID             uuid.UUID `json:"entity_id"`
	Oldest               time.Time `json:"oldest"`
	Latest               time.Time `json:"latest"`
	Path                 string    `json:"path,omitempty"`
	RowsRead             int       `json:"rows_read"`
	Truncated            bool      `json:"truncated"`
	NextCursor           string    `json:"next_cursor,omitempty"`
	HistoryStartInWindow bool      `json:"history_start_in_window"`
	LegacyRowCount       int       `json:"legacy_row_count"`
	// LegacyRelationshipEventIDs mirrors
	// [audit.LastChangeReport.LegacyRelationshipEventIDs].
	LegacyRelationshipEventIDs []uuid.UUID             `json:"legacy_relationship_event_ids"`
	Fields                     []audit.LastChangeEntry `json:"fields"`
	Relationships              []audit.LastChangeEntry `json:"relationships"`
}

// auditLastChangeOp declares `audit last-change`: the latest successful change
// of each field path and each relationship of one node, derived from the
// ledger rows of one org over a bounded window. It reads through the
// audit_reader role and records as audit.read.
func auditLastChangeOp(f *cli.Factory) clispec.Operation[auditLastChangeInput] {
	return clispec.Operation[auditLastChangeInput]{
		Name:     clispec.Name{Canonical: "last-change", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbAuditRead), Reads: true},
		Group:    auditGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Print the latest change of each field path and relationship of one node as JSON",
		Long: "Each entry has a status. known: the change is the latest evidence. unknown: a legacy row " +
			"that cannot name its fields is not older than the latest known change, or only legacy rows exist. " +
			"ambiguous_order: candidate changes lack a FoundationDB commit version and cannot be ordered. " +
			"partial: the rows read do not cover the node's history. The command reads at most " +
			strconv.Itoa(audit.LastChangeRowLimit) + " rows, newest first. When it stops at that limit, it sets " +
			"truncated and prints next_cursor for --cursor.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[auditLastChangeInput]{
			clispec.StringParam("org", "org_id (UUID)", "", true, func(in *auditLastChangeInput, v string) { in.Org = v }),
			clispec.StringParam("entity-id", "node ID (UUID)", "", true, func(in *auditLastChangeInput, v string) { in.EntityID = v }),
			clispec.StringParam("oldest", "RFC3339 lower bound (inclusive)", "", true, func(in *auditLastChangeInput, v string) { in.Oldest = v }),
			clispec.StringParam("latest", "RFC3339 upper bound (exclusive)", "", true, func(in *auditLastChangeInput, v string) { in.Latest = v }),
			clispec.StringParam("path", "one RFC 6901 field path, such as /props/state_id; omits relationships", "", false, func(in *auditLastChangeInput, v string) { in.Path = v }),
			clispec.StringParam("cursor", "next_cursor from a truncated read; empty starts at --latest", "", false, func(in *auditLastChangeInput, v string) { in.Cursor = v }),
		},
		New: func() auditLastChangeInput {
			return auditLastChangeInput{InputMarker: clispec.InputMarker{}, Org: "", EntityID: "", Oldest: "", Latest: "", Path: "", Cursor: ""}
		},
		Run: runAuditLastChange(f),
	}
}

func runAuditLastChange(f *cli.Factory) func(context.Context, auditLastChangeInput, clispec.ResultSink) error {
	return func(ctx context.Context, in auditLastChangeInput, sink clispec.ResultSink) error {
		const command = "audit last-change"
		orgID, err := parseAuditUUID(ctx, command, "--org", in.Org)
		if err != nil {
			return err
		}
		entityID, err := parseAuditUUID(ctx, command, "--entity-id", in.EntityID)
		if err != nil {
			return err
		}
		oldest, latest, err := parseAuditWindow(ctx, command, in.Oldest, in.Latest)
		if err != nil {
			return err
		}
		path := strings.TrimSpace(in.Path)
		if path != "" {
			if err := audit.ValidateFieldPath(path); err != nil {
				slog.ErrorContext(ctx, "audit.last_change_bad_path", slog.String("path", path), slog.String("err", err.Error()))
				return fmt.Errorf("%s: --path: %w", command, err)
			}
		}
		reader, err := openAuditReader(ctx, f, command)
		if err != nil {
			return err
		}
		defer reader.Close()
		deriver := audit.NewLastChangeDeriver(entityID, path)
		filter := audit.NodeHistoryFilter{OrgID: orgID, NodeID: entityID, Oldest: oldest, Latest: latest}
		read, err := reader.ReadNodeHistory(ctx, filter, strings.TrimSpace(in.Cursor), deriver.Add)
		if err != nil {
			slog.ErrorContext(ctx, "audit.last_change_failed", slog.String("entity_id", entityID.String()), slog.String("err", err.Error()))
			return fmt.Errorf("%s %s: %w", command, entityID, err)
		}
		report := deriver.Report(read.Truncated)
		slog.InfoContext(ctx, "audit.last_change_read", slog.String("entity_id", entityID.String()),
			slog.Int("rows_read", read.RowsRead), slog.Bool("truncated", read.Truncated))
		return clispec.WriteJSONValue(ctx, sink, auditLastChangeResult{
			Command: "audit.last_change", OrgID: orgID, EntityID: entityID, Oldest: oldest, Latest: latest, Path: path,
			RowsRead: read.RowsRead, Truncated: read.Truncated, NextCursor: read.NextCursor,
			HistoryStartInWindow: report.HistoryStartInWindow, LegacyRowCount: report.LegacyRowCount,
			LegacyRelationshipEventIDs: report.LegacyRelationshipEventIDs,
			Fields:                     report.Fields, Relationships: report.Relationships,
		})
	}
}
