package datagen

import (
	"fmt"
	"strconv"
	"strings"
)

// pageRerunNodes applies the list tools' limit and cursor, using the offset
// of the next row as the cursor.
func pageRerunNodes(nodes []rerunNode, args ToolArguments) ([]rerunNode, string) {
	offset, _ := strconv.Atoi(args.Cursor)
	offset = min(offset, len(nodes))
	if args.Limit <= 0 || offset+args.Limit >= len(nodes) {
		return nodes[offset:], ""
	}
	end := offset + args.Limit
	return nodes[offset:end], strconv.Itoa(end)
}

// writeRerunCursor prints the cursor line the server prints when more rows
// exist.
func writeRerunCursor(output *strings.Builder, cursor string) {
	if cursor != "" {
		fmt.Fprintf(output, "\nMore results exist. Next cursor: `%s`\n", cursor)
	}
}
