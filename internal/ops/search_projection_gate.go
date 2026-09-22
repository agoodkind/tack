package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/telemetry"
)

type projectionReadinessError struct {
	orgID string
	err   error
}

func (e projectionReadinessError) Error() string {
	return fmt.Sprintf("require search projections for org %s: %s", e.orgID, e.err)
}

func (e projectionReadinessError) Unwrap() error { return e.err }

func requireSearchProjections(ctx context.Context, factory *cli.Factory) error {
	env, err := NewEnv(ctx, factory.Cfg)
	if err != nil {
		wrapped := fmt.Errorf("open search projection readiness environment: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.projection.environment_failed", slog.String("err", wrapped.Error()))
		return wrapped
	}
	defer env.Close()
	var cursor string
	missing := make([]string, 0, 10)
	missingCount := 0
	for {
		definitions, next, scanErr := env.Stores.PropertyDefs.ScanProjections(ctx, cursor)
		if scanErr != nil {
			wrapped := fmt.Errorf("scan property definitions for search projection readiness: %w", scanErr)
			telemetry.L(ctx).ErrorContext(ctx, "search.projection.definitions_failed", slog.String("err", wrapped.Error()))
			return wrapped
		}
		for _, definition := range definitions {
			if definition.Search == nil {
				missingCount++
				if len(missing) < 10 {
					missing = append(missing, definition.OrgID.String()+"/"+definition.ID.String())
				}
				continue
			}
			if err := definition.Search.Validate(ctx, slog.String("property_definition_id", definition.ID.String())); err != nil {
				return projectionReadinessError{orgID: definition.OrgID.String(), err: err}
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if missingCount == 0 {
		return nil
	}
	wrapped := fmt.Errorf("missing search projections for %d definitions; first identities: %s", missingCount, strings.Join(missing, ", "))
	telemetry.L(ctx).ErrorContext(ctx, "search.projection.missing", slog.String("err", wrapped.Error()))
	return wrapped
}
