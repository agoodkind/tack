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

// readNodeIntegrityDocument requires path to be inside directory. It decodes
// one request or manifest from the file and rejects unknown fields. It then
// requires every export file path in the decoded entries to be inside
// directory.
func readNodeIntegrityDocument[Document nodeIntegrityDocument](ctx context.Context, directory repairDirectory, path string, document *Document) error {
	if err := directory.require(ctx, "document", path); err != nil {
		return err
	}
	if err := decodeNodeIntegrityDocument(ctx, path, document); err != nil {
		return err
	}
	switch decoded := any(document).(type) {
	case *NodeIntegrityRequest:
		return directory.requireExports(ctx, decoded.Entries)
	case *NodeIntegrityManifest:
		return directory.requireExports(ctx, decoded.Entries)
	}
	return nil
}

func decodeNodeIntegrityDocument[Document nodeIntegrityDocument](ctx context.Context, path string, document *Document) error {
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
