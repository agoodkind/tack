package tools

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/domain"
)

const (
	defaultListLimit = 25
	maxListLimit     = 100
)

// pageSchemaFields are the paging inputs every list-style tool accepts.
func pageSchemaFields() []schemaField {
	return []schemaField{
		{Name: "limit", Type: schemaInteger, Desc: fmt.Sprintf("Rows per page, 1 to %d. Default %d.", maxListLimit, defaultListLimit), Enum: nil},
		{Name: "cursor", Type: schemaString, Desc: "Cursor printed at the end of the previous page. Omit for the first page.", Enum: nil},
	}
}

// pageArgs reads limit and cursor. An absent limit returns defaultListLimit.
func pageArgs(args argMap) (int, string, error) {
	limit := defaultListLimit
	if raw, ok := args["limit"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &limit); err != nil {
			slog.Warn("mcp.list.limit_invalid", slog.String("err", err.Error()))
			return 0, "", fmt.Errorf("limit must be an integer: %w", domain.ErrInvalidArgument)
		}
	}
	if limit < 1 || limit > maxListLimit {
		slog.Warn("mcp.list.limit_invalid", slog.Int("limit", limit))
		return 0, "", fmt.Errorf("limit must be between 1 and %d: %w", maxListLimit, domain.ErrInvalidArgument)
	}
	return limit, optionalString(args, "cursor"), nil
}
