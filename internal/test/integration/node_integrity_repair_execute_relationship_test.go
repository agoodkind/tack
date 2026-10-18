package integration_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

const changedWatchNote = "Changed after the export"

// TestNodeIntegrityRepairExecuteDeleteStopsChangedRelationship reproduces
// a process stop immediately after the exported checkpoint commit.
// The fixture issue does not have hierarchy children and has one watches
// relationship to a second issue. The test freezes a delete entry and uses
// recordStoppedExport to write its export file and exported checkpoint.
// It then changes the relationship properties without changing the edge
// identity. The test verifies that one execution of
// ops backfill once-node-integrity --execute reports the entry as
// conflicted with the reason from exportedEdgeExpectations.
// That check compares each exported relationship, including the digest of
// its stored value, with the current relationships before DeleteNode.
// The reason assertion requires the command to accept the export file
// before running the check. The test verifies that the issue still exists,
// the exported checkpoint stays unchanged, and the relationship retains
// its changed payload.
func TestNodeIntegrityRepairExecuteDeleteStopsChangedRelationship(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	entry := deleteFixtureEntry(t, env, graph, "tack-582-delete-relationship", filepath.Join(repairDirectory(t), "relationship.json"))
	watchedID := createRepairIssue(t, env, graph.projectID, "Watched issue")
	writeWatchesEdge(t, env, entry.NodeID, watchedID, map[string]json.RawMessage{})
	manifest := freezeRepairRequest(t, repairRequest(entry))
	frozen := manifest.Entries[0]
	recordStoppedExport(t, env, manifest, frozen)
	before, err := env.Stores.Repairs().RepairCheckpoint(env.Ctx, entry.RepairID)
	if err != nil || before == nil {
		t.Fatalf("read checkpoint %s = %+v, %v", entry.RepairID, before, err)
	}
	writeWatchesEdge(t, env, entry.NodeID, watchedID, map[string]json.RawMessage{"note": textValue(changedWatchNote)})

	applied, err := executeRepairManifest(t, manifest)
	if err != nil {
		t.Fatalf("execute after the relationship changed: %v", err)
	}
	outcome := requireApplyStatus(t, applied, entry.RepairID, ops.RepairApplyConflicted)
	if !strings.Contains(outcome.Reason, "differs from the export") {
		t.Fatalf("conflict reason = %q, want the exported edge check", outcome.Reason)
	}
	requireRepairNodePresent(t, env, entry.NodeID)
	after, err := env.Stores.Repairs().RepairCheckpoint(env.Ctx, entry.RepairID)
	if err != nil || after == nil || *after != *before {
		t.Fatalf("checkpoint %s = %+v, %v, want the unchanged exported checkpoint %+v", entry.RepairID, after, err, before)
	}
	edges, err := env.Stores.Relationships.ListBySource(env.Ctx, env.OrgID, entry.NodeID, node.RelWatches)
	if err != nil || len(edges) != 1 || propText(t, edges[0].Props, "note") != changedWatchNote {
		t.Fatalf("watches edges of %s = %+v, %v, want the changed relationship", entry.NodeID, edges, err)
	}
}

// writeWatchesEdge writes a watches relationship from sourceID to targetID
// with props through the relationship store. A second call for the same
// pair replaces the stored payload and preserves the edge identity.
func writeWatchesEdge(t *testing.T, env *integration.TestEnv, sourceID, targetID uuid.UUID, props map[string]json.RawMessage) {
	t.Helper()
	relationship := &node.Relationship{
		OrgID: env.OrgID, SourceID: sourceID, RelationType: node.RelWatches, TargetID: targetID,
		CreatedBy: uuid.New(), CreatedAt: time.Now().UTC(), Props: props,
	}
	if err := env.Stores.Relationships.Add(env.Ctx, relationship); err != nil {
		t.Fatalf("write the watches edge of %s: %v", sourceID, err)
	}
}

// recordStoppedExport writes the state that exportPhase leaves after it
// commits the exported checkpoint. It reads the node through
// RepairStore.RepairExport and writes the export document to the entry's
// export path. It records the exported checkpoint with the file's SHA-256
// digest through RepairStore.AdvanceRepairCheckpoint.
func recordStoppedExport(t *testing.T, env *integration.TestEnv, manifest ops.NodeIntegrityManifest, entry ops.NodeIntegrityEntry) {
	t.Helper()
	repairs := env.Stores.Repairs()
	record, err := repairs.RepairExport(env.Ctx, entry.NodeID)
	if err != nil || !record.Found {
		t.Fatalf("read the export record of %s = %+v, %v", entry.NodeID, record, err)
	}
	document := stoppedExportDocument{
		RepairID: entry.RepairID, ManifestDigest: manifest.Digest, NodeID: entry.NodeID,
		PrimaryDigest: entry.Source.PrimaryDigest, Primary: record.Primary, Relationships: record.Relationships,
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatalf("encode the export of %s: %v", entry.NodeID, err)
	}
	if err := os.WriteFile(entry.Delete.ExportFile, encoded, 0o600); err != nil {
		t.Fatalf("write the export of %s: %v", entry.NodeID, err)
	}
	sum := sha256.Sum256(encoded)
	checkpoint := node.RepairCheckpoint{
		RepairID: entry.RepairID, ManifestDigest: manifest.Digest, NodeID: entry.NodeID, SourceDigest: entry.Source.PrimaryDigest,
		Phase: node.RepairPhaseExported, ExportPath: entry.Delete.ExportFile, ExportDigest: hex.EncodeToString(sum[:]),
		ActorID: uuid.MustParse(repairOperatorID), UpdatedAt: time.Now().UTC(),
	}
	if err := repairs.AdvanceRepairCheckpoint(env.Ctx, "", checkpoint); err != nil {
		t.Fatalf("record the exported checkpoint of %s: %v", entry.RepairID, err)
	}
}

// stoppedExportDocument contains only the export document fields that
// deletePhase reads. exportRepairNode in internal/ops defines the complete
// export file format, and verifyRepairExport decodes the file.
type stoppedExportDocument struct {
	RepairID       string                        `json:"repair_id"`
	ManifestDigest string                        `json:"manifest_digest"`
	NodeID         uuid.UUID                     `json:"node_id"`
	PrimaryDigest  string                        `json:"primary_digest"`
	Primary        json.RawMessage               `json:"primary"`
	Relationships  []fdbadapter.RepairExportEdge `json:"relationships"`
}
