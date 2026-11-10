package ops

import (
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
)

var (
	_ clispec.ReadResult = (*inspectReadResult)(nil)
	_ clispec.ReadResult = (*inspectListResult)(nil)
	_ clispec.ReadResult = (*repairVerifyResult)(nil)
	_ clispec.ReadResult = (*repairValidateResult)(nil)
	_ clispec.ReadResult = (*previewOutput)(nil)
	_ clispec.ReadResult = (*authTokenListResult)(nil)
)

func (r inspectReadResult) AuditReadTargets() ([]audit.Target, error) {
	return []audit.Target{audit.NodeTarget(r.NodeID)}, nil
}

func (r inspectListResult) AuditReadTargets() ([]audit.Target, error) {
	targets := make([]audit.Target, 0, len(r.Views))
	for _, view := range r.Views {
		if view != nil {
			targets = append(targets, audit.NodeTarget(view.ID))
		}
	}

	return targets, nil
}

func (r repairVerifyResult) AuditReadTargets() ([]audit.Target, error) {
	return []audit.Target{audit.NodeTarget(r.NodeID)}, nil
}

func (r repairValidateResult) AuditReadTargets() ([]audit.Target, error) {
	nodeID, err := uuid.Parse(r.NodeID)
	if err != nil {
		slog.Error("ops.validate_target_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("ops validate node: the result node ID is not a UUID: %w", err)
	}

	return []audit.Target{audit.NodeTarget(nodeID)}, nil
}

func (r previewOutput) AuditReadTargets() ([]audit.Target, error) {
	if r.Preview == nil {
		return []audit.Target{}, nil
	}

	return []audit.Target{audit.NodeTarget(r.Preview.NodeID)}, nil
}

func (r authTokenListResult) AuditReadTargets() ([]audit.Target, error) {
	targets := make([]audit.Target, 0, len(r.Tokens))
	for _, entry := range r.Tokens {
		tokenID, err := uuid.Parse(entry.TokenID)
		if err != nil {
			slog.Error("ops.token_list_target_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("ops auth token-list: a listed token ID is not a UUID: %w", err)
		}

		targets = append(targets, postgres.TokenTarget(tokenID, entry.Label))
	}

	return targets, nil
}
