package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

func yugabyteRuntimeFiles(ctx context.Context, password string) (map[string][]byte, error) {
	root, err := repoRoot(ctx)
	if err != nil {
		return nil, err
	}
	const runtimeInputPath = "/run/secrets/YUGABYTE_PASSWORD"
	files := make(map[string][]byte)
	files[runtimeInputPath] = []byte(password)
	assets := []struct{ source, target string }{
		{"entrypoint.sh", "/opt/tack/yugabyte-entrypoint.sh"},
		{"healthcheck.sh", "/opt/tack/yugabyte-healthcheck.sh"},
		{"secret.sh", "/opt/tack/yugabyte-secret.sh"},
		{"healthcheck.sql", "/opt/tack/yugabyte-healthcheck.sql"},
	}
	for _, asset := range assets {
		contents, err := os.ReadFile(filepath.Join(root, "yugabyte-overlay", asset.source))
		if err != nil {
			wrapped := fmt.Errorf("read Yugabyte runtime script %s: %w", asset.source, err)
			slog.ErrorContext(ctx, "testenv.ledger_script_failed", slog.String("err", wrapped.Error()))
			return nil, wrapped
		}
		files[asset.target] = contents
	}
	return files, nil
}
