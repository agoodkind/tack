package ops

import (
	"reflect"
	"strings"
	"testing"
)

// importSnapshotOutput renders `yb-admin import_snapshot` output the way
// ImportSnapshotMetaFile in YugabyteDB 2024.2's yb-admin_client.cc prints it:
// the object type padded to sixteen characters, then the old and new ids, the
// three separated by " \t", with tablet rows numbered. rows are the mapping
// rows after the header.
func importSnapshotOutput(rows ...string) string {
	return "Snapshot metadata file: /artifacts/metadata.snapshot\n" +
		"Importing snapshot 4963ed18-2295-4e4f-9d9f-0a1a45c39bd1 (COMPLETE)\n" +
		"Table type: table\n" +
		"Target imported table name: tack.users\n" +
		"Successfully applied snapshot.\n" +
		"Object           \tOld ID                           \tNew ID                          \n" +
		strings.Join(rows, "\n") + "\n" +
		"Snapshot         \t4963ed18-2295-4e4f-9d9f-0a1a45c39bd1 \tb1e5d6c2-0a3f-4c8e-9d1b-2f3a4b5c6d7e\n"
}

func mappingRow(object, oldID, newID string) string {
	return object + strings.Repeat(" ", 16-len(object)) + " \t" + oldID + " \t" + newID
}

// The table ids below are in the form GetPgsqlTableId renders (the database
// oid in the first four bytes, the table oid in the last four); the tablet ids
// are patterned rather than random so no scanner reads them as credentials.
const (
	usersTableID    = "000033e800003000800000000000400a"
	tokensTableID   = "000033e8000030008000000000004010"
	usersTabletOld  = "aaaa0000aaaa0000aaaa0000aaaa0001"
	usersTabletNew  = "bbbb0000bbbb0000bbbb0000bbbb0001"
	tokensTabletOld = "aaaa0000aaaa0000aaaa0000aaaa0002"
	tokensTabletNew = "bbbb0000bbbb0000bbbb0000bbbb0002"
)

// TestParseYBSnapshotMappingReadsTheEngineRows proves the parser reads the
// rows the engine prints, pairing each tablet with the table row above it.
func TestParseYBSnapshotMappingReadsTheEngineRows(t *testing.T) {
	out := importSnapshotOutput(
		mappingRow("Keyspace", "000033e8000030008000000000000000", "000033e8000030008000000000000000"),
		mappingRow("Table", usersTableID, usersTableID),
		mappingRow("Tablet 0", usersTabletOld, usersTabletNew),
		mappingRow("Table", tokensTableID, tokensTableID),
		mappingRow("Tablet 0", tokensTabletOld, tokensTabletNew),
	)

	remaps, err := parseYBSnapshotMapping(out)
	if err != nil {
		t.Fatalf("parseYBSnapshotMapping: %v", err)
	}

	want := []ybTabletRemap{
		{table: usersTableID, old: usersTabletOld, new: usersTabletNew},
		{table: tokensTableID, old: tokensTabletOld, new: tokensTabletNew},
	}
	if !reflect.DeepEqual(remaps, want) {
		t.Fatalf("remaps:\n got=%+v\nwant=%+v", remaps, want)
	}
}

// TestParseYBSnapshotMappingRefusesRowsItCannotPlace is the silent skip this
// test exists for. A tablet row short of its ids, or one before any table row,
// matched no case and fell out of the mapping: a tablet the placement never
// tried and the audit never counted, since both work from the mapping. Each
// row below must refuse the whole mapping with the row named.
func TestParseYBSnapshotMappingRefusesRowsItCannotPlace(t *testing.T) {
	tableRow := mappingRow("Table", usersTableID, usersTableID)
	for _, tc := range []struct {
		name   string
		before []string
		row    string
	}{
		{name: "a tablet row short of its new id", before: []string{tableRow}, row: "Tablet 0        \t" + usersTabletOld},
		{name: "a tablet row with no ids", before: []string{tableRow}, row: "Tablet 0"},
		{name: "a tablet row before any table row", before: nil, row: mappingRow("Tablet 0", usersTabletOld, usersTabletNew)},
		{name: "a table row with no id", before: nil, row: "Table"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := append(append([]string{}, tc.before...), tc.row, mappingRow("Tablet 1", tokensTabletOld, tokensTabletNew))
			out := importSnapshotOutput(rows...)

			remaps, err := parseYBSnapshotMapping(out)

			if err == nil {
				t.Fatalf("a mapping row the placement cannot use must be refused, got %+v", remaps)
			}
			if remaps != nil {
				t.Fatalf("a refused mapping must yield no rows for the placement, got %+v", remaps)
			}
			if !strings.Contains(err.Error(), strings.Join(strings.Fields(tc.row), " ")) {
				t.Fatalf("the refusal must name the row, got: %v", err)
			}
		})
	}
}

// TestParseYBSnapshotMappingRefusesIdsOutsideTheEngineForm is the path
// traversal this file exists for. The ids are fields of engine output, but
// that output is produced by importing a metadata file staged from the object
// store, so a crafted file controls them, and the placement embeds them in
// paths under the scratch cluster's data directory. Rather than looking for
// the traversal, the parser requires the form the engine emits, and every
// value below, traversal or not, is refused with the row named.
func TestParseYBSnapshotMappingRefusesIdsOutsideTheEngineForm(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  string
	}{
		{name: "parent directory as a new tablet id", row: mappingRow("Tablet 0", usersTabletOld, "../../../../var/lib/evil")},
		{name: "parent directory as an old tablet id", row: mappingRow("Tablet 0", "..", usersTabletNew)},
		{name: "parent directory as a table id", row: mappingRow("Table", "../..", "../..")},
		{name: "a separator inside a tablet id", row: mappingRow("Tablet 0", usersTabletOld, "bbbb0000/bbb0000bbbb0000bbbb0001")},
		{name: "uppercase hexadecimal", row: mappingRow("Tablet 0", usersTabletOld, strings.ToUpper(usersTabletNew))},
		{name: "one character short", row: mappingRow("Tablet 0", usersTabletOld, usersTabletNew[1:])},
		{name: "a dashed uuid", row: mappingRow("Tablet 0", usersTabletOld, "4963ed18-2295-4e4f-9d9f-0a1a45c39bd1")},
		{name: "a colocated parent table id", row: mappingRow("Table", usersTableID+".colocated.parent.uuid", usersTableID)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := importSnapshotOutput(
				mappingRow("Table", usersTableID, usersTableID),
				tc.row,
				mappingRow("Tablet 1", tokensTabletOld, tokensTabletNew),
			)

			remaps, err := parseYBSnapshotMapping(out)

			if err == nil {
				t.Fatalf("a mapping row outside the engine's form must be refused, got %+v", remaps)
			}
			if remaps != nil {
				t.Fatalf("a refused mapping must yield no rows for the placement, got %+v", remaps)
			}
			if !strings.Contains(err.Error(), strings.Join(strings.Fields(tc.row), " ")) {
				t.Fatalf("the refusal must name the row, got: %v", err)
			}
		})
	}
}
