package search

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/telemetry"
)

// ServerVersion reads the OpenSearch distribution and version through the
// typed API and returns the version string.
//
// The response must identify the opensearch distribution and include a
// nonempty version.
//
//   - An API failure logs and returns the request error.
//   - A different distribution or an empty version logs and returns a
//     validation error.
func (a *Adapter) ServerVersion(ctx context.Context) (string, error) {
	info, err := a.api.Info(ctx, nil)
	if err != nil {
		wrapped := fmt.Errorf("read OpenSearch server version: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.server.version_failed", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	if info.Version.Distribution != "opensearch" || info.Version.Number == "" {
		wrapped := fmt.Errorf("unexpected search distribution %q version %q", info.Version.Distribution, info.Version.Number)
		telemetry.L(ctx).ErrorContext(ctx, "search.server.version_invalid", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	return info.Version.Number, nil
}
