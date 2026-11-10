package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
)

var errStrictJSONTrailingData = errors.New("data follows the first JSON value")

type nodeIntegrityDocument interface {
	NodeIntegrityRequest | NodeIntegrityManifest
}

// readNodeIntegrityDocument requires path to be inside directory. It decodes
// one request or manifest from the file and rejects unknown fields. It then
// requires every export file path in the decoded entries to be inside
// directory.
func readNodeIntegrityDocument[Document nodeIntegrityDocument](
	ctx context.Context,
	directory repairDirectory,
	path string,
	document *Document,
) error {
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

func decodeNodeIntegrityDocument[Document nodeIntegrityDocument](
	ctx context.Context,
	path string,
	document *Document,
) error {
	content, err := os.ReadFile(path)
	if err != nil {
		wrapped := fmt.Errorf("open node integrity document %q: %w", path, err)
		slog.ErrorContext(
			ctx,
			"repair.manifest.open_failed",
			slog.String("err", wrapped.Error()),
			slog.String("path", path),
		)

		return wrapped
	}

	err = decodeStrictJSON(ctx, content, document)
	if err != nil {
		wrapped := fmt.Errorf("decode node integrity document %q: %w", path, err)
		slog.ErrorContext(
			ctx,
			"repair.manifest.decode_failed",
			slog.String("err", wrapped.Error()),
			slog.String("path", path),
		)

		return wrapped
	}

	return nil
}

type strictJSONTarget interface {
	NodeIntegrityRequest | NodeIntegrityManifest | AuditPartRestoreManifest
}

func decodeStrictJSON[Target strictJSONTarget](
	ctx context.Context,
	content []byte,
	target *Target,
) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(target)
	if err != nil {
		slog.ErrorContext(ctx, "repair.strict_json.decode_failed", slog.String("err", err.Error()))

		return fmt.Errorf("cannot decode JSON: %w", err)
	}

	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		trailingErr := errStrictJSONTrailingData
		slog.ErrorContext(
			ctx,
			"repair.strict_json.trailing_data",
			slog.String("err", trailingErr.Error()),
		)

		return trailingErr
	}

	return nil
}
