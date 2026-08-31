package ops

import (
	"context"
	"errors"
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
	// FailAfterRenames stops the run once it has applied that many renames,
	// so a partway failure can be produced against a real store on a testbed.
	// Zero disables it. The CLI refuses the flag outside a QA or local target,
	// so a production run cannot reach this (TACK-452).
	FailAfterRenames int
}

// ErrRepairFaultInjected is what a run stopped by FailAfterRenames returns.
// It is a real failure by design: the point is to leave applied renames behind
// and prove the command still records them.
var ErrRepairFaultInjected = errors.New("repair stopped by the injected testbed fault")

// ReferenceRename records one repaired node reference.
type ReferenceRename struct {
	OrgID  uuid.UUID
	NodeID uuid.UUID
	From   string
	To     string
}

// ReferenceCounterSeed records one sequence counter the repair seeded to its
// scope's high-water mark.
type ReferenceCounterSeed struct {
	OrgID uuid.UUID
	Key   string
	Value int64
}

// ReferenceKeyWrite records one reference key the repair claimed for a node.
type ReferenceKeyWrite struct {
	OrgID        uuid.UUID
	NodeID       uuid.UUID
	NodeType     string
	TemplateName string
	Encoded      string
}

// RepairReferenceReport records the work one repair run planned or applied.
// Each slice names its items rather than counting them, because the ledger
// records one event per item and a count cannot be turned back into one.
type RepairReferenceReport struct {
	Renumbered []ReferenceRename
	Counters   []ReferenceCounterSeed
	Keys       []ReferenceKeyWrite
}

func runRepairReferenceUniqueness(ctx context.Context, env *Env) error {
	report, err := RepairReferenceUniqueness(ctx, env, RepairReferenceOptions{
		Execute:          false,
		Keep:             keepOldest,
		FailAfterRenames: 0,
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
		slog.Int("counters_seeded", len(report.Counters)),
		slog.Int("keys_written", len(report.Keys)),
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
		Renumbered: nil,
		Counters:   nil,
		Keys:       nil,
	}
	orgIDs, err := listOrgIDs(ctx, env)
	if err != nil {
		return report, err
	}
	// Every stage returns what it applied even when it fails, and the partial
	// work is folded into the report before the error goes up. A run that dies
	// midway has already written those changes to FoundationDB, so the report
	// the caller records from has to carry them (TACK-452).
	for orgID := range orgIDs {
		seeded, seedErr := seedReferenceCounters(ctx, env, orgID, opts.Execute)
		report.Counters = append(report.Counters, seeded...)
		if seedErr != nil {
			return report, seedErr
		}
	}
	duplicates, err := FindDuplicateReferences(ctx, env)
	if err != nil {
		return report, err
	}
	applied := 0
	beforeRename := func() error {
		if opts.FailAfterRenames > 0 && applied >= opts.FailAfterRenames {
			faultErr := fmt.Errorf("%w after %d rename(s)", ErrRepairFaultInjected, applied)
			env.Log.WarnContext(ctx, "repair.reference_uniqueness.fault_injected",
				slog.Int("applied_renames", applied),
				slog.String("err", faultErr.Error()))
			return faultErr
		}
		applied++
		return nil
	}
	for _, duplicate := range duplicates {
		renamed, renameErr := renumberDuplicateGroup(ctx, env, duplicate, opts, beforeRename)
		report.Renumbered = append(report.Renumbered, renamed...)
		if renameErr != nil {
			return report, renameErr
		}
	}
	if !opts.Execute {
		return report, nil
	}
	for orgID := range orgIDs {
		written, writeErr := writeAllReferenceKeys(ctx, env, orgID)
		report.Keys = append(report.Keys, written...)
		if writeErr != nil {
			return report, writeErr
		}
	}
	return report, nil
}
