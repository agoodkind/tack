package ops

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// seedReferenceCounters returns the counters it raised, so the caller can
// record one ledger event per seed that happened. A counter already at or
// above its scope's highest number is left alone and left out, because a seed
// that changes nothing is not a mutation and must not read as one in the
// ledger. A dry run reads the same counters and returns the ones an execute
// would raise, without writing anything.
func seedReferenceCounters(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	execute bool,
) ([]ReferenceCounterSeed, error) {
	counters, err := enumerateReferenceCounters(ctx, env, orgID, nil)
	if err != nil {
		return nil, err
	}
	seeded := make([]ReferenceCounterSeed, 0, len(counters))
	for _, counter := range counters {
		raised, raiseErr := raiseReferenceCounter(ctx, env, orgID, counter, execute)
		if raiseErr != nil {
			// The counters already raised are returned with the error, for the
			// same reason the renames are: they are live in the store and the
			// caller owes each one a ledger row (TACK-452).
			return seeded, raiseErr
		}
		if !raised {
			continue
		}
		seeded = append(seeded, ReferenceCounterSeed{
			OrgID: orgID, Key: counter.Key, Value: counter.Value,
		})
	}
	return seeded, nil
}

// raiseReferenceCounter raises one counter to its scope's highest number and
// reports whether that changed it. In a dry run it only reads the counter and
// reports whether an execute would change it.
func raiseReferenceCounter(
	ctx context.Context,
	env *Env,
	orgID uuid.UUID,
	counter referenceCounter,
	execute bool,
) (bool, error) {
	if !execute {
		current, err := env.Stores.Nodes.PeekSequenceByKey(ctx, orgID, counter.Key)
		if err != nil {
			wrapped := fmt.Errorf("read counter %q in org %s: %w", counter.Key, orgID, err)
			env.Log.WarnContext(
				ctx, "repair.reference_uniqueness.counter_read_failed",
				slog.String("org_id", orgID.String()),
				slog.String("counter_key", counter.Key),
				slog.String("err", wrapped.Error()),
			)
			return false, wrapped
		}
		return current < counter.Value, nil
	}
	raised, err := env.Stores.Nodes.RaiseSequenceByKey(ctx, orgID, counter.Key, counter.Value)
	if err != nil {
		wrapped := fmt.Errorf("seed counter %q in org %s: %w", counter.Key, orgID, err)
		env.Log.WarnContext(
			ctx, "repair.reference_uniqueness.counter_seed_failed",
			slog.String("org_id", orgID.String()),
			slog.String("counter_key", counter.Key),
			slog.String("err", wrapped.Error()),
		)
		return false, wrapped
	}
	return raised, nil
}
