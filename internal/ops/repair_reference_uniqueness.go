package ops

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
)

const (
	keepOldest = "oldest"
	keepNewest = "newest"
)

func init() {
	Register(Operation{
		Name:        "repair.reference_uniqueness",
		Audit:       audit.Spec{Verb: string(audit.VerbOpsRepairReferenceUniqueness), Reads: true},
		Description: "Report planned reference repairs without writing.",
		Run:         runRepairReferenceUniqueness,
	})
}

// RepairReferenceOptions configures one reference uniqueness repair.
type RepairReferenceOptions struct {
	// Execute applies the repair. False reports planned changes only.
	Execute bool
	// Keep selects the retained node: oldest or newest. Empty keeps oldest.
	Keep string
}

// ReferenceRename records one repaired node reference.
type ReferenceRename struct {
	NodeID uuid.UUID
	From   string
	To     string
}

// RepairReferenceReport records the work one repair run planned or applied.
type RepairReferenceReport struct {
	Renumbered     []ReferenceRename
	CountersSeeded int
	KeysWritten    int
}

func runRepairReferenceUniqueness(ctx context.Context, env *Env) error {
	report, err := RepairReferenceUniqueness(ctx, env, RepairReferenceOptions{
		Execute: false,
		Keep:    keepOldest,
	})
	if err != nil {
		return err
	}
	for _, rename := range report.Renumbered {
		env.Log.DebugContext(ctx, "reference.rename.planned",
			slog.String("node_id", rename.NodeID.String()),
			slog.String("from", rename.From),
			slog.String("to", rename.To),
		)
	}
	env.Log.InfoContext(ctx, "repair.reference_uniqueness.completed",
		slog.Int("renumbered", len(report.Renumbered)),
		slog.Int("counters_seeded", report.CountersSeeded),
		slog.Int("keys_written", report.KeysWritten),
	)
	return nil
}

// RepairReferenceUniqueness seeds counters, renumbers duplicates, and writes
// reference keys. It applies no changes unless opts.Execute is true.
func RepairReferenceUniqueness(
	ctx context.Context,
	env *Env,
	opts RepairReferenceOptions,
) (RepairReferenceReport, error) {
	if opts.Keep == "" {
		opts.Keep = keepOldest
	}
	if opts.Keep != keepOldest && opts.Keep != keepNewest {
		return RepairReferenceReport{}, fmt.Errorf(
			"keep policy %q: want %q or %q", opts.Keep, keepOldest, keepNewest,
		)
	}
	report := RepairReferenceReport{
		Renumbered:     nil,
		CountersSeeded: 0,
		KeysWritten:    0,
	}
	orgIDs, err := listOrgIDs(ctx, env)
	if err != nil {
		return report, err
	}
	for orgID := range orgIDs {
		seeded, seedErr := seedReferenceCounters(ctx, env, orgID, opts.Execute)
		if seedErr != nil {
			return report, seedErr
		}
		report.CountersSeeded += seeded
	}
	duplicates, err := FindDuplicateReferences(ctx, env)
	if err != nil {
		return report, err
	}
	for _, duplicate := range duplicates {
		renamed, renameErr := renumberDuplicateGroup(ctx, env, duplicate, opts)
		if renameErr != nil {
			return report, renameErr
		}
		report.Renumbered = append(report.Renumbered, renamed...)
	}
	if !opts.Execute {
		return report, nil
	}
	for orgID := range orgIDs {
		written, writeErr := writeAllReferenceKeys(ctx, env, orgID)
		if writeErr != nil {
			return report, writeErr
		}
		report.KeysWritten += written
	}
	return report, nil
}
