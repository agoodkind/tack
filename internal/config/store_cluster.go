package config

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/caarlos0/env/v11"
	"goodkind.io/tack/internal/telemetry"
)

// StoreClusterSettings locate the product store's cluster file for the
// one-shot fdbcli containers the ops commands run. The loader reads each
// value from the environment or its default.
type StoreClusterSettings struct {
	// ClusterFileDirectory is the host directory that contains fdb.cluster.
	// A one-shot mounts it read-write, because fdbcli records a coordinator
	// change in the file.
	ClusterFileDirectory string `env:"TACK_OPS_FDB_CLUSTER_DIR" envDefault:"/etc/foundationdb"`
}

// LoadStoreClusterSettings parses the cluster file location and rejects a
// relative directory, which Docker would read as a named volume.
func LoadStoreClusterSettings(ctx context.Context) (StoreClusterSettings, error) {
	var settings StoreClusterSettings
	if err := env.Parse(&settings); err != nil {
		wrapped := fmt.Errorf("parse store cluster settings: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "ops.store.config_failed", slog.String("err", wrapped.Error()))
		return StoreClusterSettings{}, wrapped
	}
	if !filepath.IsAbs(settings.ClusterFileDirectory) {
		wrapped := fmt.Errorf("TACK_OPS_FDB_CLUSTER_DIR %q is not an absolute path", settings.ClusterFileDirectory)
		telemetry.L(ctx).ErrorContext(ctx, "ops.store.config_failed", slog.String("err", wrapped.Error()))
		return StoreClusterSettings{}, wrapped
	}
	return settings, nil
}
