package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// repairReportContent is the encoding of the input for computing the report
// content digest.
type repairReportContent struct {
	NodeType string                     `json:"node_type"`
	Name     string                     `json:"name"`
	Props    map[string]json.RawMessage `json:"props"`
}

// reportContentDigest returns the SHA-256 digest of nodeType, name, and the
// compact values in props for the properties that report declares. The
// digest excludes other properties, such as parent_id and scope_id, that
// comment creation adds.
func reportContentDigest(ctx context.Context, report RepairReport, nodeType, name string, props map[string]json.RawMessage) (string, error) {
	declared := make(map[string]json.RawMessage, len(report.Props))
	for key := range report.Props {
		var compact bytes.Buffer
		if err := json.Compact(&compact, props[key]); err != nil {
			return "", reportDigestFailure(ctx, report, fmt.Sprintf("property %q is absent or is not JSON", key), domain.ErrConflict)
		}
		declared[key] = compact.Bytes()
	}
	encoded, err := json.Marshal(repairReportContent{NodeType: nodeType, Name: name, Props: declared})
	if err != nil {
		return "", reportDigestFailure(ctx, report, "the report content cannot be encoded", domain.ErrInvalidArgument)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// reportPreservesSource requires a text value in the report props to contain
// the complete text of the source property in the frozen primary record.
func reportPreservesSource(ctx context.Context, entry NodeIntegrityEntry) error {
	report := *entry.Report
	var primary node.Node
	if err := json.Unmarshal(entry.Source.Primary, &primary); err != nil {
		return reportDigestFailure(ctx, report, "the frozen primary record is not a node record", domain.ErrInvalidArgument)
	}
	var source string
	if json.Unmarshal(primary.Props[report.SourceProperty], &source) != nil || source == "" {
		return reportDigestFailure(ctx, report, "the frozen source property is not text", domain.ErrInvalidArgument)
	}
	for _, value := range report.Props {
		var text string
		if json.Unmarshal(value, &text) == nil && strings.Contains(text, source) {
			return nil
		}
	}
	return reportDigestFailure(ctx, report, "the report props do not contain the complete source text", domain.ErrInvalidArgument)
}

func reportDigestFailure(ctx context.Context, report RepairReport, reason string, sentinel error) error {
	wrapped := fmt.Errorf("digest the report of property %s: %s: %w", report.SourceProperty, reason, sentinel)
	slog.ErrorContext(ctx, "repair.report.digest_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
