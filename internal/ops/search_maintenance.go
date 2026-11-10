package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/config"
)

func newSearchMaintenanceAdapter(ctx context.Context, cfg *config.Config) (*search.Adapter, error) {
	credentials, err := config.LoadSearchMaintenanceCredentials()
	if err != nil {
		wrapped := fmt.Errorf("load OpenSearch maintenance credentials: %w", err)
		slog.ErrorContext(ctx, "search.maintenance_credentials_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	certificate, err := config.LoadSearchCA(ctx, cfg.SearchCA)
	if err != nil {
		wrapped := fmt.Errorf("load maintenance search CA: %w", err)
		slog.ErrorContext(ctx, "search.maintenance_ca_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	adapter, err := search.New(ctx, search.Config{
		Endpoint: cfg.SearchEndpoint, CA: certificate,
		Username: credentials.Username, Password: credentials.Password,
		RequestTimeout: cfg.SearchRequestTimeout, MaxRetries: cfg.SearchMaxRetries,
	})
	if err != nil {
		wrapped := fmt.Errorf("create maintenance search adapter: %w", err)
		slog.ErrorContext(ctx, "search.maintenance_adapter_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	return adapter, nil
}
