package ops

import (
	"reflect"
	"strings"
	"testing"
)

// importSnapshotOutput reproduces ImportSnapshotMetaFile output from
// YugabyteDB 2024.2's yb-admin_client.cc: padded object types, tab-separated
// old and new IDs, and numbered tablet rows after the header.
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

// Table IDs follow GetPgsqlTableId: database OID first and table OID last.
// Patterned tablet IDs avoid credential-scanner false positives.
const (
	usersTableID    = "000033e800003000800000000000400a"
	tokensTableID   = "000033e8000030008000000000004010"
	usersTabletOld  = "aaaa0000aaaa0000aaaa0000aaaa0001"
	usersTabletNew  = "bbbb0000bbbb0000bbbb0000bbbb0001"
	tokensTabletOld = "aaaa0000aaaa0000aaaa0000aaaa0002"
	tokensTabletNew = "bbbb0000bbbb0000bbbb0000bbbb0002"
)

// TestParseYBSnapshotMappingReadsTheEngineRows checks table association and
// old-to-new tablet IDs in engine-formatted rows.
func TestParseYBSnapshotMappingReadsTheEngineRows(t *testing.T) {
	out := importSnapshotOutput(
		mappingRow(
			"Keyspace",
			"000033e8000030008000000000000000",
			"000033e8000030008000000000000000",
		),
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

// TestParseYBSnapshotMappingRefusesRowsItCannotPlace checks complete mapping
// rejection for missing IDs, invalid table association, and changed table IDs.
// Silently omitting a row would exclude its tablet from placement and audit.
func TestParseYBSnapshotMappingRefusesRowsItCannotPlace(t *testing.T) {
	tableRow := mappingRow("Table", usersTableID, usersTableID)
	for _, tc := range []struct {
		name   string
		before []string
		row    string
	}{
		{
			name:   "tablet row omits new ID",
			before: []string{tableRow},
			row:    "Tablet 0        \t" + usersTabletOld,
		},
		{name: "tablet row omits both IDs", before: []string{tableRow}, row: "Tablet 0"},
		{
			name:   "tablet row precedes table rows",
			before: nil,
			row:    mappingRow("Tablet 0", usersTabletOld, usersTabletNew),
		},
		{name: "table row omits ID", before: nil, row: "Table"},
		{
			name:   "table row changes preserved ID",
			before: nil,
			row:    mappingRow("Table", usersTableID, tokensTableID),
		},
		{
			name:   "table row includes invalid new ID",
			before: nil,
			row:    mappingRow("Table", usersTableID, "../.."),
		},
	} {
		t.Run(
			tc.name,
			func(t *testing.T) {
				rows := append(
					append([]string{}, tc.before...),
					tc.row,
					mappingRow("Tablet 1", tokensTabletOld, tokensTabletNew),
				)
				out := importSnapshotOutput(rows...)

				remaps, err := parseYBSnapshotMapping(out)
				if err == nil {
					t.Fatalf("an unusable mapping row must fail validation, got %+v", remaps)
				}

				if remaps != nil {
					t.Fatalf("a rejected mapping must not return placement rows, got %+v", remaps)
				}

				if !strings.Contains(err.Error(), strings.Join(strings.Fields(tc.row), " ")) {
					t.Fatalf("the refusal must identify the row, got: %v", err)
				}
			},
		)
	}
}

// TestParseYBSnapshotMappingAcceptsSingleTableID checks the permitted short
// table row without a repeated ID.
func TestParseYBSnapshotMappingAcceptsSingleTableID(t *testing.T) {
	out := importSnapshotOutput(
		"Table            \t"+usersTableID,
		mappingRow("Tablet 0", usersTabletOld, usersTabletNew),
	)

	remaps, err := parseYBSnapshotMapping(out)
	if err != nil {
		t.Fatalf("parseYBSnapshotMapping: %v", err)
	}

	want := []ybTabletRemap{{table: usersTableID, old: usersTabletOld, new: usersTabletNew}}
	if !reflect.DeepEqual(remaps, want) {
		t.Fatalf("remaps:\n got=%+v\nwant=%+v", remaps, want)
	}
}

// TestParseYBSnapshotMappingRefusesIdsOutsideTheEngineForm checks IDs that
// could create unsafe paths or violate the engine's format. Object-store
// metadata controls imported IDs, which placement uses under the scratch
// data directory. Invalid IDs reject the whole mapping and identify the row.
func TestParseYBSnapshotMappingRefusesIdsOutsideTheEngineForm(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  string
	}{
		{
			name: "parent directory as a new tablet id",
			row:  mappingRow("Tablet 0", usersTabletOld, "../../../../var/lib/evil"),
		},
		{
			name: "parent directory as an old tablet id",
			row:  mappingRow("Tablet 0", "..", usersTabletNew),
		},
		{name: "parent directory as a table id", row: mappingRow("Table", "../..", "../..")},
		{
			name: "a separator inside a tablet id",
			row:  mappingRow("Tablet 0", usersTabletOld, "bbbb0000/bbb0000bbbb0000bbbb0001"),
		},
		{
			name: "uppercase hexadecimal",
			row:  mappingRow("Tablet 0", usersTabletOld, strings.ToUpper(usersTabletNew)),
		},
		{
			name: "one character short",
			row:  mappingRow("Tablet 0", usersTabletOld, usersTabletNew[1:]),
		},
		{
			name: "a dashed uuid",
			row:  mappingRow("Tablet 0", usersTabletOld, "4963ed18-2295-4e4f-9d9f-0a1a45c39bd1"),
		},
		{
			name: "a colocated parent table id",
			row:  mappingRow("Table", usersTableID+".colocated.parent.uuid", usersTableID),
		},
	} {
		t.Run(
			tc.name,
			func(t *testing.T) {
				out := importSnapshotOutput(
					mappingRow("Table", usersTableID, usersTableID),
					tc.row,
					mappingRow("Tablet 1", tokensTabletOld, tokensTabletNew),
				)

				remaps, err := parseYBSnapshotMapping(out)
				if err == nil {
					t.Fatalf("a mapping row outside the engine's form must fail, got %+v", remaps)
				}

				if remaps != nil {
					t.Fatalf("a rejected mapping must not return placement rows, got %+v", remaps)
				}

				if !strings.Contains(err.Error(), strings.Join(strings.Fields(tc.row), " ")) {
					t.Fatalf("the refusal must identify the row, got: %v", err)
				}
			},
		)
	}
}
