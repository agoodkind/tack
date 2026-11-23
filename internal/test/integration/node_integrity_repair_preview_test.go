package integration_test

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityRepairPreviewPreservesStoredKeysAndValues checks that command dry runs
// freeze and preview a repair without changing FoundationDB keys or values.
// The preview must report the entry as planned.
func TestNodeIntegrityRepairPreviewPreservesStoredKeysAndValues(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	before := storedKeyValues(t)

	manifest := freezeRepairRequest(t, repairRequest(graph.selectEpicEntry("tack-582-select-epic")))
	preview := previewRepairManifest(t, manifest)

	if !maps.Equal(before, storedKeyValues(t)) {
		t.Fatal("the freeze or the preview changed stored keys")
	}

	if preview.ManifestDigest != manifest.Digest || len(manifest.Entries) != 1 {
		t.Fatalf(
			"preview digest %s and manifest %+v do not match",
			preview.ManifestDigest,
			manifest,
		)
	}

	entry := previewEntry(t, preview, "tack-582-select-epic")
	if entry.Status != ops.RepairPreviewPlanned || entry.NextPhase != node.RepairPhaseCompleted {
		t.Fatalf("preview entry = %+v, want planned with next phase completed", entry)
	}

	source := manifest.Entries[0].Source
	if source == nil || len(source.Chain) < 2 || source.Chain[0] != graph.epicID ||
		source.Chain[1] != graph.projectID {
		t.Fatalf("frozen source = %+v, want the chain epic, project", source)
	}
}

// TestNodeIntegrityRepairPreviewReportsStaleEvidence checks that changes to
// the primary record, a related record, or metadata after freezing produce
// a conflicted preview with a reason identifying the stale evidence.
func TestNodeIntegrityRepairPreviewReportsStaleEvidence(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, env *integration.TestEnv, graph repairGraph)
		want   string
	}{
		{
			name: "primary",
			change: func(t *testing.T, env *integration.TestEnv, graph repairGraph) {
				renameNode(t, env, graph.issueID)
			},
			want: "stale primary record",
		},
		{
			name: "graph",
			change: func(t *testing.T, env *integration.TestEnv, graph repairGraph) {
				renameNode(t, env, graph.epicID)
			},
			want: "stale graph",
		},
		{
			name: "metadata",
			change: func(t *testing.T, env *integration.TestEnv, _ repairGraph) {
				changePropertyDefinition(t, env)
			},
			want: "stale metadata",
		},
	}
	for _, scenario := range cases {
		t.Run(
			scenario.name,
			func(t *testing.T) {
				env := integration.SetupTestEnv(t)
				graph := newRepairGraph(t, env)
				manifest := freezeRepairRequest(
					t,
					repairRequest(graph.selectEpicEntry("tack-582-stale-"+scenario.name)),
				)
				scenario.change(t, env, graph)
				entry := previewEntry(
					t,
					previewRepairManifest(t, manifest),
					"tack-582-stale-"+scenario.name,
				)
				if entry.Status != ops.RepairPreviewConflicted ||
					!strings.Contains(entry.Reason, scenario.want) {
					t.Fatalf("preview entry = %+v, want conflicted with %q", entry, scenario.want)
				}
			},
		)
	}
}

// TestNodeIntegrityRepairRejectsUnreviewedChanges checks that the command
// rejects an edited frozen manifest and a request that keeps the current
// project parent and removes the child_of edge to that project.
// The test also forges a related record digest in the frozen source and
// recomputes the manifest digest. The command must reject that manifest
// because the graph digest no longer matches the frozen edges, related
// records, and chain.
func TestNodeIntegrityRepairRejectsUnreviewedChanges(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	manifest := freezeRepairRequest(t, repairRequest(graph.selectEpicEntry("tack-582-edited")))
	forged := manifest
	forged.Entries = []ops.NodeIntegrityEntry{manifest.Entries[0]}
	source := *manifest.Entries[0].Source
	source.Related = append([]ops.RepairRelated{}, source.Related...)
	source.Related[0].PrimaryDigest = strings.Repeat("0", 64)
	forged.Entries[0].Source = &source
	forged.Digest = manifestDigest(t, forged)
	if _, err := runRepairCommand(
		t,
		"--manifest",
		writeRepairDocument(t, "forged.json", forged),
	); err == nil ||
		!strings.Contains(err.Error(), "graph digest") {
		t.Fatalf("preview of forged graph evidence = %v, want a graph digest mismatch", err)
	}

	manifest.Entries[0].Result.Props["priority"] = json.RawMessage(`"high"`)
	if _, err := runRepairCommand(
		t,
		"--manifest",
		writeRepairDocument(t, "edited.json", manifest),
	); err == nil ||
		!strings.Contains(err.Error(), "manifest digest") {
		t.Fatalf("preview of an edited manifest = %v, want a digest mismatch", err)
	}

	keepProject := graph.selectEpicEntry("tack-582-wrong-edge")
	keepProject.Result.Props = map[string]json.RawMessage{}
	keepProject.Result.RemoveEdges = []ops.RepairEdge{
		{RelationType: node.RelChildOf, TargetID: graph.projectID},
	}
	if _, err := runRepairCommand(
		t,
		"--request",
		writeRepairDocument(t, "wrong.json", repairRequest(keepProject)),
	); err == nil ||
		!strings.Contains(err.Error(), "removes the child_of edge to the resulting parent") {
		t.Fatalf("freeze of a removal of the resulting parent edge = %v, want a refusal", err)
	}
}
