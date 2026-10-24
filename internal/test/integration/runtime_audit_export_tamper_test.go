package integration_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// exportTamper is one edit to the exported row with ID eventID and the
// failure text that verification must report for that row.
type exportTamper struct {
	eventID uuid.UUID
	mutate  func(*audit.Row)
	failure string
}

// manifestTampers returns edits that remove, reorder, or alter the manifest
// parts of the read event readID. The altered part has a matching part
// digest.
func manifestTampers(t *testing.T, readID uuid.UUID) map[string]exportTamper {
	t.Helper()
	parts := func(mutate func([]audit.ManifestPart) []audit.ManifestPart) exportTamper {
		return exportTamper{eventID: readID, failure: "read manifest", mutate: func(row *audit.Row) {
			if len(row.ManifestParts) < 2 {
				t.Fatalf("exported row %s has %d manifest parts, want at least 2", readID, len(row.ManifestParts))
			}
			row.ManifestParts = mutate(row.ManifestParts)
		}}
	}
	return map[string]exportTamper{
		"missing manifest part": parts(func(list []audit.ManifestPart) []audit.ManifestPart { return list[:len(list)-1] }),
		"reordered manifest parts": parts(func(list []audit.ManifestPart) []audit.ManifestPart {
			list[0], list[1] = list[1], list[0]
			return list
		}),
		"altered manifest part": parts(func(list []audit.ManifestPart) []audit.ManifestPart {
			list[0].Payload = bytes.Replace(list[0].Payload, []byte(`"issue"`), []byte(`"epic"`), 1)
			digest := sha256.Sum256(list[0].Payload)
			list[0].Digest = hex.EncodeToString(digest[:])
			return list
		}),
	}
}

// changeTampers returns edits to the typed relationship target and the
// delta of the change event changeID. Each target edit also sets the entity
// ID column to the edited target ID.
func changeTampers(t *testing.T, changeID uuid.UUID) map[string]exportTamper {
	t.Helper()
	target := func(edit func(*audit.Target)) exportTamper {
		return exportTamper{eventID: changeID, failure: "hash mismatch", mutate: func(row *audit.Row) {
			if row.Context.Target == nil {
				t.Fatalf("exported row %s has no target", changeID)
			}
			edit(row.Context.Target)
			row.EntityID = row.Context.Target.ID
		}}
	}
	return map[string]exportTamper{
		"altered target id": target(func(edited *audit.Target) { edited.ID = uuid.Must(uuid.NewV7()) }),
		"altered relationship target": target(func(edited *audit.Target) {
			edited.TargetID = uuid.Must(uuid.NewV7())
		}),
		"altered delta new value": {eventID: changeID, failure: "hash mismatch", mutate: func(row *audit.Row) {
			if row.Delta == nil || len(row.Delta.After) == 0 {
				t.Fatalf("exported row %s has no delta new value", changeID)
			}
			row.Delta.After = json.RawMessage(`{"target_id":"` + uuid.Must(uuid.NewV7()).String() + `"}`)
		}},
	}
}

// tamperExportedRow applies mutate to the exported row with ID eventID and
// returns the rows with that row encoded again.
func tamperExportedRow(t *testing.T, rows []byte, eventID uuid.UUID, mutate func(*audit.Row)) []byte {
	t.Helper()
	lines := bytes.Split(bytes.TrimSuffix(rows, []byte("\n")), []byte("\n"))
	output := make([]byte, 0, len(rows))
	found := false
	for _, line := range lines {
		var row audit.Row
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatalf("decode an exported row: %v", err)
		}
		if row.EventID == eventID {
			found = true
			mutate(&row)
			encoded, err := json.Marshal(row)
			if err != nil {
				t.Fatalf("encode the tampered row: %v", err)
			}
			line = encoded
		}
		output = append(append(output, line...), '\n')
	}
	if !found {
		t.Fatalf("the exported rows do not include event %s", eventID)
	}
	return output
}
