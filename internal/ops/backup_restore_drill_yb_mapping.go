// backup_restore_drill_yb_mapping.go reads the tablet mapping out of
// `yb-admin import_snapshot` output and is where the ids in it are contained.
// They are fields of engine output, but that output is produced by importing a
// metadata file staged from the object store, so a crafted file controls them,
// and the placement builds paths under the scratch cluster's data directory
// from them. The form the engine emits is required here, at the boundary, so
// nothing downstream has to look for what an id might do.

package ops

import (
	"fmt"
	"regexp"
	"strings"
)

// ybTabletRemap is one row of the import_snapshot mapping: the table id is
// preserved while the tablet id is reassigned, so the export's tablet files
// (named by the old tablet id) are copied into the new tablet's snapshot dir.
type ybTabletRemap struct {
	table string
	old   string
	new   string
}

// ybObjectIDPattern is the form of every table and tablet id the import
// mapping names: 32 lowercase hexadecimal characters, a 16-byte UUID rendered
// without dashes. That is what the engine generates for both, per the
// YugabyteDB 2024.2 source: a tablet id comes from GenerateObjectId
// (src/yb/util/oid_generator.cc, b2a_hex of a random UUID), and a YSQL table
// id from GetPgsqlTableId (src/yb/common/entity_ids.cc, UuidToString, whose
// IsPgsqlId check requires exactly 32 characters). The colocated parent table
// forms, which append ".colocated.parent.uuid" and the like, do not arise
// because the tack database is not colocated, and a row in any other form is
// refused by name rather than placed.
//
// Requiring the form at the parse boundary is what contains the ids: they
// come out of `yb-admin import_snapshot`, whose input is the metadata file
// staged from the object store, and the placement builds source and
// destination paths from them. An id in this form is one path component, so
// no value that reaches the placement can name a parent directory or a
// separator.
var ybObjectIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// parseYBSnapshotMapping extracts the table-id-preserved, tablet-id-remapped
// rows from import_snapshot output. The mapping section begins at the line
// starting with "Object"; a "Table" row sets the current table id and each
// following "Tablet" row pairs an old tablet id with its new one. yb-admin
// prints each row as the object type, the old id, and the new id separated by
// " \t" (ImportSnapshotMetaFile in src/yb/tools/yb-admin_client.cc), with the
// tablet rows numbered, which is why a tablet's ids are its third and fourth
// fields.
//
// Every id is required to have the engine's form before it is kept, a table
// row must carry its id, and a tablet row must carry both of its ids and
// follow a table row. The whole mapping is refused on the first row that does
// not, naming it, because a row dropped in silence would be a tablet the
// placement never tries and the audit never misses.
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
			if len(fields) < ybTableRowFields {
				return nil, ybMappingRowError(fields, "names no table id")
			}
			if err := requireYBObjectIDs(fields, fields[1]); err != nil {
				return nil, err
			}
			table = fields[1]
		case fields[0] == "Tablet":
			if len(fields) < ybTabletRowFields {
				return nil, ybMappingRowError(fields, "does not name both an old and a new tablet id")
			}
			if table == "" {
				return nil, ybMappingRowError(fields, "precedes every Table row, so its tablet belongs to no table")
			}
			if err := requireYBObjectIDs(fields, fields[2], fields[3]); err != nil {
				return nil, err
			}
			remaps = append(remaps, ybTabletRemap{table: table, old: fields[2], new: fields[3]})
		}
	}
	return remaps, nil
}

const (
	// ybTableRowFields is the fewest fields a Table row carries: the word and
	// the preserved table id.
	ybTableRowFields = 2
	// ybTabletRowFields is the fewest fields a Tablet row carries: the word,
	// its number, the old tablet id, and the new one.
	ybTabletRowFields = 4
)

// requireYBObjectIDs refuses a mapping row carrying an id that is not in the
// engine's form, naming the row's fields and the id.
func requireYBObjectIDs(row []string, ids ...string) error {
	for _, id := range ids {
		if !ybObjectIDPattern.MatchString(id) {
			return ybMappingRowError(row, fmt.Sprintf("names %q, which is not a 32-character"+
				" hexadecimal table or tablet id", id))
		}
	}
	return nil
}

// ybMappingRowError refuses one mapping row, naming its fields and what is
// wrong with it.
func ybMappingRowError(row []string, reason string) error {
	return fmt.Errorf("import_snapshot mapping row %q %s", strings.Join(row, " "), reason)
}
