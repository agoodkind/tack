// The snapshot mapping parser reads tablet mappings from `yb-admin
// import_snapshot` output. The import reads metadata staged from the object
// store, and a crafted metadata file can control these IDs. Placement uses the
// IDs to build paths under the scratch cluster's data directory. The parser
// requires the engine's ID format before placement uses an ID.

package ops

import (
	"fmt"
	"regexp"
	"strings"
)

// ybTabletRemap represents one import_snapshot mapping row. The import
// preserves the table ID and assigns a new tablet ID. Placement copies
// exported files identified by the old tablet ID into the new tablet's snapshot
// directory.
type ybTabletRemap struct {
	table string
	old   string
	new   string
}

// ybObjectIDPattern matches a 16-byte UUID as 32 lowercase hexadecimal
// characters without dashes. Both table and tablet IDs in the import mapping
// use this format. In the YugabyteDB 2024.2 source, GenerateObjectId in
// src/yb/util/oid_generator.cc uses b2a_hex on a random UUID for tablet IDs.
// GetPgsqlTableId in src/yb/common/entity_ids.cc uses UuidToString for YSQL
// table IDs, and IsPgsqlId requires exactly 32 characters.
//
// Colocated parent table IDs append suffixes such as ".colocated.parent.uuid".
// The original parser comment assumes that the tack database does not use
// colocation. The parser rejects other formats and includes the offending row
// in the error. A live observation against 2024.2.8.0 confirmed that `yb-admin
// list_all_tablet_servers` prints server IDs in this format. The export server
// listing uses this pattern too. Snapshot IDs use the dashed format that
// ybUUIDPattern matches.
//
// The import reads metadata staged from the object store. Placement constructs
// source and destination paths from the resulting IDs. Each accepted ID forms
// one path component and cannot contain a separator or identify a parent
// directory.
var ybObjectIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// parseYBSnapshotMapping reads preserved table IDs and remapped tablet IDs
// from import_snapshot output. A line starting with "Object" begins the mapping
// section. Each "Table" row selects the current table, and each following
// "Tablet" row pairs an old tablet ID with a new ID. ImportSnapshotMetaFile in
// src/yb/tools/yb-admin_client.cc prints the object type, old ID, and new ID
// separated by " \t". Numbered tablet rows put the IDs in the third and fourth
// fields.
//
// The parser requires the engine's ID format for every accepted ID. A table
// row must include its ID; if it includes a second ID, both IDs must match.
// A tablet row must include both IDs and follow a table row. The parser
// rejects the whole mapping at the first invalid row and includes that row in
// the error. Silently omitting a tablet row would omit that tablet from both
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
					"does not name both an old and a new tablet id",
				)
			}

			if table == "" {
				return nil, ybMappingRowError(
					fields,
					"precedes every Table row, so its tablet belongs to no table",
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

// parseYBMappingTableRow reads the preserved ID from a Table row. The engine
// prints old and new IDs with the same value because imports preserve YSQL
// table IDs. The parser rejects differing IDs rather than selecting one for
// placement paths. It also accepts a row with just the preserved ID.
func parseYBMappingTableRow(fields []string) (string, error) {
	if len(fields) < ybTableRowFields {
		return "", ybMappingRowError(fields, "names no table id")
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
			"names a new table id that differs from its old one, which the import preserves",
		)
	}

	return old, nil
}

const (
	// ybTableRowFields sets the minimum Table row length: the object type and
	// preserved table ID.
	ybTableRowFields = 2
	// ybTabletRowFields sets the minimum Tablet row length: the object type,
	// row number, old tablet ID, and new tablet ID.
	ybTabletRowFields = 4
)

// requireYBObjectIDs rejects a mapping row if any ID does not match the
// engine's format. The error includes the row's fields and the invalid ID.
func requireYBObjectIDs(row []string, ids ...string) error {
	for _, objectID := range ids {
		if !ybObjectIDPattern.MatchString(objectID) {
			return ybMappingRowError(
				row,
				fmt.Sprintf(
					"names %q, which is not a 32-character"+
						" hexadecimal table or tablet id",
					objectID,
				),
			)
		}
	}

	return nil
}

// ybMappingRowError reports a rejected mapping row with its fields and the
// reason for rejection.
func ybMappingRowError(row []string, reason string) error {
	return fmt.Errorf("import_snapshot mapping row %q %s", strings.Join(row, " "), reason)
}
