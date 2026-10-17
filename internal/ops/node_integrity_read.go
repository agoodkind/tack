package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
)

type nodeIntegrityDocument interface {
	NodeIntegrityRequest | NodeIntegrityManifest
}

func readNodeIntegrityDocument[Document nodeIntegrityDocument](ctx context.Context, path string, document *Document) error {
	file, err := os.Open(path)
	if err != nil {
		wrapped := fmt.Errorf("open node integrity document %q: %w", path, err)
		slog.ErrorContext(ctx, "repair.manifest.open_failed", slog.String("err", wrapped.Error()), slog.String("path", path))
		return wrapped
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(document); err != nil {
		wrapped := fmt.Errorf("decode node integrity document %q: %w", path, err)
		slog.ErrorContext(ctx, "repair.manifest.decode_failed", slog.String("err", wrapped.Error()), slog.String("path", path))
		return wrapped
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		wrapped := fmt.Errorf("node integrity document %q has data after its first JSON value", path)
		slog.ErrorContext(ctx, "repair.manifest.decode_failed", slog.String("err", wrapped.Error()), slog.String("path", path))
		return wrapped
	}
	return nil
}
