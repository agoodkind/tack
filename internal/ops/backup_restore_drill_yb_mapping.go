// Snapshot import mapping validation restricts table and tablet IDs before
// placement uses them in paths under the scratch cluster's data directory.
// A crafted metadata file from the object store can control these IDs.

package ops

import (
	"fmt"
	"regexp"
	"strings"
)

// ybTabletRemap records a preserved table ID and old and new tablet IDs.
// Placement copies the old tablet's exported files into the new snapshot
// directory.
type ybTabletRemap struct {
	table string
	old   string
	new   string
}

// ybObjectIDPattern matches 32 lowercase hexadecimal characters, the engine's
// table and tablet ID format. YugabyteDB 2024.2 generates tablet IDs with
// GenerateObjectId in src/yb/util/oid_generator.cc using b2a_hex of a random
// UUID. GetPgsqlTableId in src/yb/common/entity_ids.cc uses UuidToString for
// YSQL tables; IsPgsqlId requires exactly 32 characters. Tack does not use
// colocated tables with suffixes such as ".colocated.parent.uuid".
// list_all_tablet_servers also prints this format, as verified on 2024.2.8.0.
// Snapshot IDs instead use the dashed format that ybUUIDPattern matches.
//
// Object-store metadata controls the import mapping. Restricting IDs to one
// hexadecimal path component prevents directory traversal and separators in
// source and destination paths.
var ybObjectIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// parseYBSnapshotMapping reads preserved table IDs and remapped tablet IDs.
// The mapping begins with "Object". A "Table" row sets the current table ID;
// following "Tablet" rows pair old and new IDs. ImportSnapshotMetaFile in
// src/yb/tools/yb-admin_client.cc separates fields with " \t" and numbers
// tablet rows. Tablet IDs occupy the third and fourth fields.
//
// Every ID must match the engine's format. A table row must include its ID
// and repeat that ID if it includes a new one. A tablet row must include both
// IDs and follow a table row. Validation rejects the entire mapping at the
// first invalid row. Silently omitting a row would exclude that tablet from
// placement and its audit.
func parseYBSnapshotMapping(out string) ([]ybTabletRemap, error) {
	var remaps []ybTabletRemap
	started := false
	table := ""

	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}

		switch {
		case fields[0] == "Object":
			started = true
		case !started:
			continue
		case fields[0] == "Table":
			tableID, err := parseYBMappingTableRow(fields)
			if err != nil {
				return nil, err
			}

			table = tableID
		case fields[0] == "Tablet":
			if len(fields) < ybTabletRowFields {
				return nil, ybMappingRowError(
					fields,
					"does not include both an old and a new tablet id",
				)
			}

			if table == "" {
				return nil, ybMappingRowError(
					fields,
					"precedes every Table row and does not identify its table",
				)
			}

			if err := requireYBObjectIDs(fields, fields[2], fields[3]); err != nil {
				return nil, err
			}

			remaps = append(remaps, ybTabletRemap{table: table, old: fields[2], new: fields[3]})
		}
	}

	return remaps, nil
}

// parseYBMappingTableRow accepts a table ID once or twice. Snapshot import
// preserves YSQL table IDs. Validation rejects different old and new IDs
// rather than selecting one for placement paths.
func parseYBMappingTableRow(fields []string) (string, error) {
	if len(fields) < ybTableRowFields {
		return "", ybMappingRowError(fields, "does not include a table id")
	}

	old := fields[1]
	if err := requireYBObjectIDs(fields, old); err != nil {
		return "", err
	}

	if len(fields) == ybTableRowFields {
		return old, nil
	}

	newID := fields[2]
	if err := requireYBObjectIDs(fields, newID); err != nil {
		return "", err
	}

	if newID != old {
		return "", ybMappingRowError(
			fields,
			"includes a new table id that differs from its old one, which the import preserves",
		)
	}

	return old, nil
}

const (
	// ybTableRowFields counts the minimum fields: "Table" and the table ID.
	ybTableRowFields = 2
	// ybTabletRowFields counts "Tablet", its number, and both tablet IDs.
	ybTabletRowFields = 4
)

// requireYBObjectIDs rejects IDs outside the engine's format and reports the
// invalid ID and row fields.
func requireYBObjectIDs(row []string, ids ...string) error {
	for _, id := range ids {
		if !ybObjectIDPattern.MatchString(id) {
			return ybMappingRowError(
				row,
				fmt.Sprintf(
					"includes %q, which is not a 32-character"+
						" hexadecimal table or tablet id",
					id,
				),
			)
		}
	}

	return nil
}

// ybMappingRowError reports the row fields and validation failure.
func ybMappingRowError(row []string, reason string) error {
	return fmt.Errorf("import_snapshot mapping row %q %s", strings.Join(row, " "), reason)
}
