package ops

import (
	"archive/tar"
	"reflect"
	"strings"
	"testing"
)

// TestYBArchiveInventoryRecordsEveryFileWithItsSize proves the record is taken
// from the archive itself: every regular member is listed at the size it was
// archived with, directory entries carry no bytes and are left out, and the
// "./" the export's find prefixes every path with is gone, so the paths are the
// ones the restore compares its extraction against.
func TestYBArchiveInventoryRecordsEveryFileWithItsSize(t *testing.T) {
	entries := tabletArchiveEntries("table-a/tablet-1.snapshots/snap",
		map[string]string{"CURRENT": "abc", "000123.sst": strings.Repeat("x", 4096)})
	entries = append(entries, tabletArchiveEntries("table-a/tablet-2.snapshots/snap",
		map[string]string{"CURRENT": "de"})...)

	inventory, err := readYBArchiveInventory(t.Context(), "20260830T010203Z", "yb1", writeTestArchive(t, entries))
	if err != nil {
		t.Fatalf("readYBArchiveInventory: %v", err)
	}

	want := []ybArchivedFile{
		{Path: "table-a/tablet-1.snapshots/snap/000123.sst", Size: 4096},
		{Path: "table-a/tablet-1.snapshots/snap/CURRENT", Size: 3},
		{Path: "table-a/tablet-2.snapshots/snap/CURRENT", Size: 2},
	}
	if !reflect.DeepEqual(inventory.Files, want) {
		t.Fatalf("inventory files:\n got=%+v\nwant=%+v", inventory.Files, want)
	}
	if inventory.RunID != "20260830T010203Z" || inventory.Node != "yb1" {
		t.Fatalf("inventory declares run %q node %q", inventory.RunID, inventory.Node)
	}
}

// TestYBArchiveInventoryRefusesArchivesItCannotDescribe proves the export fails
// rather than publishing an archive whose record is silently short. A member
// the inventory skips is a member the restore never checks, which is the
// silence the whole record exists to remove, and an archive of directory
// entries alone carries no tablet data at all.
func TestYBArchiveInventoryRefusesArchivesItCannotDescribe(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []testArchiveEntry
	}{
		{
			name: "only directory entries",
			entries: []testArchiveEntry{
				{name: "./table-a/tablet-1.snapshots/snap/", typeflag: tar.TypeDir, body: ""},
			},
		},
		{
			name: "a member that is not a file",
			entries: append(
				tabletArchiveEntries("table-a/tablet-1.snapshots/snap", map[string]string{"CURRENT": "abc"}),
				testArchiveEntry{
					name: "./table-a/tablet-1.snapshots/snap/link", typeflag: tar.TypeSymlink, body: "CURRENT",
				}),
		},
		{
			name: "a name no inventory line can hold",
			entries: []testArchiveEntry{
				{name: "./table-a/tablet-1.snapshots/snap/od\td", typeflag: tar.TypeReg, body: "abc"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := readYBArchiveInventory(t.Context(), "20260830T010203Z", "yb1", writeTestArchive(t, tc.entries)); err == nil {
				t.Fatal("the export must refuse an archive it cannot fully describe")
			}
		})
	}
}

// TestYBArchiveInventoryRoundTrip proves the record the export writes is the
// record the restore reads, field for field, through the same bytes that go to
// the object store.
func TestYBArchiveInventoryRoundTrip(t *testing.T) {
	inventory := ybArchiveInventory{
		RunID: "20260830T010203Z",
		Node:  "yb1",
		Files: []ybArchivedFile{
			{Path: "table-a/tablet-1.snapshots/snap/CURRENT", Size: 3},
			{Path: "table-a/tablet-1.snapshots/snap/000123.sst", Size: 4096},
		},
	}

	decoded, err := parseYBArchiveInventory("20260830T010203Z", "yb1", inventory.render())
	if err != nil {
		t.Fatalf("parseYBArchiveInventory: %v", err)
	}
	if !reflect.DeepEqual(decoded, inventory) {
		t.Fatalf("round trip:\n got=%+v\nwant=%+v", decoded, inventory)
	}
}

// TestParseYBArchiveInventoryRefusesWhatWouldWeakenTheCheck is the point of the
// header. An inventory that arrives short, or that belongs to another run or
// node, expects the wrong set of files, and every tablet judged against the
// wrong set is judged too leniently. Each shape below must be refused rather
// than read as a smaller archive.
func TestParseYBArchiveInventoryRefusesWhatWouldWeakenTheCheck(t *testing.T) {
	whole := ybArchiveInventory{
		RunID: "20260830T010203Z",
		Node:  "yb1",
		Files: []ybArchivedFile{
			{Path: "table-a/tablet-1.snapshots/snap/CURRENT", Size: 3},
			{Path: "table-a/tablet-1.snapshots/snap/000123.sst", Size: 4096},
		},
	}
	body := string(whole.render())
	truncated := body[:strings.LastIndex(strings.TrimSuffix(body, "\n"), "\n")+1]

	for _, tc := range []struct {
		name  string
		runID string
		node  string
		body  string
	}{
		{name: "truncated", runID: "20260830T010203Z", node: "yb1", body: truncated},
		{name: "another run", runID: "20260830T999999Z", node: "yb1", body: body},
		{name: "another node", runID: "20260830T010203Z", node: "yb2", body: body},
		{name: "no header", runID: "20260830T010203Z", node: "yb1", body: "table-a/x\t3"},
		{name: "empty", runID: "20260830T010203Z", node: "yb1", body: ""},
		{
			name: "a line with no size", runID: "20260830T010203Z", node: "yb1",
			body: ybInventoryMarker + " 20260830T010203Z yb1 1\ntable-a/x\n",
		},
		{
			name: "a size that is not a number", runID: "20260830T010203Z", node: "yb1",
			body: ybInventoryMarker + " 20260830T010203Z yb1 1\ntable-a/x\tsome\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseYBArchiveInventory(tc.runID, tc.node, []byte(tc.body)); err == nil {
				t.Fatalf("parsing %q as the inventory for run %s node %s must fail", tc.body, tc.runID, tc.node)
			}
		})
	}
}
