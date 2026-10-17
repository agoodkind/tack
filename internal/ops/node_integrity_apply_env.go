package ops

import (
	"context"
	"errors"
	"log/slog"

	"goodkind.io/tack/internal/audit"
)

func applyNodeIntegrityManifest(ctx context.Context, env *Env, manifest NodeIntegrityManifest) (NodeIntegrityApply, error) {
	empty := NodeIntegrityApply{ManifestDigest: manifest.Digest, Counts: map[RepairApplyStatus]int{}, Entries: []NodeIntegrityEntryApply{}}
	principal, ok := audit.OperatorPrincipalFromContext(ctx)
	if !ok {
		err := errors.New("apply the node integrity manifest: the operator principal is missing")
		slog.ErrorContext(ctx, "repair.apply_failed", slog.String("err", err.Error()))
		return empty, err
	}
	repairs := env.Stores.Repairs()
	applier := repairApplier{
		manifest: manifest, state: repairs, principal: principal,
		phases: repairPhaseRunner{checkpoints: repairs, exports: repairs, mutator: env.Stores.Nodes},
	}
	return applier.applyFrozenManifest(ctx)
}
