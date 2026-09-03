package ops

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// measureReferenceShape reads back what the reconstruction will derive from
// the corpus, through the same enumeration the reconstruction uses.
func measureReferenceShape(ctx context.Context, env *Env, orgID uuid.UUID) (int, int, error) {
	counters, err := enumerateReferenceCounters(ctx, env, orgID, &referenceRepairStart)
	if err != nil {
		return 0, 0, err
	}
	keys, err := enumerateReferenceKeys(ctx, env, orgID, &referenceRepairStart)
	if err != nil {
		return 0, 0, err
	}
	return len(counters), len(keys), nil
}

// checkReferenceShape refuses to call a commit good unless the org now holds
// what the report describes. The live counts are the ones that matter to the
// repair: a corpus with the right keys and no collisions gives the repair
// nothing to rename, which is the state TACK-475 found, and a corpus with
// more collisions than the shape gives it work the evidence never recorded.
func checkReferenceShape(result datagenReferenceShapeResult) error {
	if result.CounterKeys != recordedCounterSeeds {
		return fmt.Errorf("the corpus derives %d counter seeds, want %d",
			result.CounterKeys, recordedCounterSeeds)
	}
	want := recordedReferenceKeys + recordedFollowupReferenceKey
	if result.ReferenceKeys != want {
		return fmt.Errorf("the corpus derives %d reference keys, want %d", result.ReferenceKeys, want)
	}
	if result.LiveCollisions != result.Collisions {
		return fmt.Errorf("the org holds %d colliding references after writing, the shape describes %d",
			result.LiveCollisions, result.Collisions)
	}
	if result.LiveRenames != result.Renames {
		return fmt.Errorf("the org holds %d nodes for the repair to rename after writing, the shape describes %d",
			result.LiveRenames, result.Renames)
	}
	return nil
}
