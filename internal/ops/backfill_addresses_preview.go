package ops

import (
	"context"
	"errors"
	"fmt"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

func init() {
	Register(Operation{
		Name:        "backfill.addresses.preview",
		Description: "Preview legacy slug_index rows as generic primary address_index writes without writing state.",
		Run:         runAddressBackfillPreview,
	})
	Register(Operation{
		Name:        "backfill.addresses.apply",
		Description: "Apply legacy slug_index rows into generic primary address_index rows. Requires TACK_BACKFILL_APPLY=true.",
		Run:         runAddressBackfillApply,
	})
}

func runAddressBackfillPreview(ctx context.Context, env *Env) error {
	controls, err := readAddressBackfillControls()
	if err != nil {
		return err
	}
	rows, truncated, err := discoverAddressBackfillRows(ctx, controls, env)
	if err != nil {
		return err
	}
	result, err := buildAddressBackfillResult(
		ctx,
		"backfill.addresses.preview",
		"preview",
		controls,
		rows,
		truncated,
		env.Stores.Inspect,
		env.Stores.Nodes,
	)
	if err != nil {
		return err
	}
	return writeRepairOutput(result)
}

func runAddressBackfillApply(ctx context.Context, env *Env) error {
	controls, err := readAddressBackfillControls()
	if err != nil {
		return err
	}
	if err := requireAddressBackfillApply(&controls); err != nil {
		return err
	}
	rows, truncated, err := discoverAddressBackfillRows(ctx, controls, env)
	if err != nil {
		return err
	}
	result, err := buildAddressBackfillResult(
		ctx,
		"backfill.addresses.apply",
		"apply",
		controls,
		rows,
		truncated,
		env.Stores.Inspect,
		env.Stores.Nodes,
	)
	if err != nil {
		return err
	}
	applyErr := applyAddressBackfill(ctx, env.Stores.Nodes, result)
	if outputErr := writeRepairOutput(result); outputErr != nil {
		return outputErr
	}
	return applyErr
}

func applyAddressBackfill(ctx context.Context, addresses addressBackfillAddressStore, result *addressBackfillResult) error {
	if result.ConflictCount > 0 || result.MalformedCount > 0 {
		result.Warnings = append(result.Warnings, "apply stopped because conflicts or malformed candidates are present")
		return fmt.Errorf(
			"address backfill blocked by %d conflicts and %d malformed candidates",
			result.ConflictCount,
			result.MalformedCount,
		)
	}
	for i := range result.Candidates {
		candidate := &result.Candidates[i]
		if candidate.Status != addressBackfillStatusWrite {
			continue
		}
		err := addresses.WriteAddress(
			ctx,
			candidate.Target.NodeType,
			node.AddressKindPrimary,
			candidate.Target.AddressValue,
			candidate.Target.NodeID,
		)
		if err != nil {
			if errors.Is(err, domain.ErrAlreadyExists) {
				candidate.Status = addressBackfillStatusConflict
				candidate.Errors = append(candidate.Errors, err.Error())
				result.recount()
				return errors.New("write address " + candidate.Target.NodeType + "/" + candidate.Target.AddressValue + ": " + err.Error())
			}
			candidate.Errors = append(candidate.Errors, err.Error())
			result.recount()
			return errors.New("write address " + candidate.Target.NodeType + "/" + candidate.Target.AddressValue + ": " + err.Error())
		}
		candidate.Status = addressBackfillStatusWritten
	}
	result.recount()
	return nil
}

func (result *addressBackfillResult) recount() {
	result.SourceCount = len(result.Candidates)
	result.CandidateCount = len(result.Candidates)
	result.WriteCount = 0
	result.WrittenCount = 0
	result.IdempotentCount = 0
	result.ConflictCount = 0
	result.MalformedCount = 0
	result.SkippedCount = 0
	for _, candidate := range result.Candidates {
		switch candidate.Status {
		case addressBackfillStatusWrite:
			result.WriteCount++
		case addressBackfillStatusWritten:
			result.WrittenCount++
		case addressBackfillStatusIdempotent:
			result.IdempotentCount++
			result.SkippedCount++
		case addressBackfillStatusConflict:
			result.ConflictCount++
			result.SkippedCount++
		case addressBackfillStatusMalformed:
			result.MalformedCount++
			result.SkippedCount++
		}
	}
}
