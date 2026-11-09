package integration_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/ops"
)

const (
	partRestoreInvalidInputCode = "input_invalid"
	partRestoreRefusedCode      = "restore_refused"
)

var partRestoreMandatoryMembers = []string{
	"schema_version", "ticket", "org_id", "target_event_id", "source_event_id", "part_index", "manifest_sha256",
	"declared_parts", "identity_count", "target_seq", "target_shard", "target_hash_version",
	"target_row_hash", "source_seq", "source_shard", "source_row_hash",
}

func syntheticPartRestoreManifest(t *testing.T) ops.AuditPartRestoreManifest {
	t.Helper()
	root := sha256.Sum256([]byte("synthetic manifest " + uuid.NewString()))
	rootText := hex.EncodeToString(root[:])

	return ops.AuditPartRestoreManifest{
		SchemaVersion: 1, Ticket: "TACK-620", OrgID: uuid.Must(uuid.NewV7()), TargetEventID: uuid.Must(uuid.NewV7()),
		SourceEventID: uuid.Must(uuid.NewV7()), PartIndex: 0, ManifestSHA256: rootText,
		DeclaredParts: 1, IdentityCount: partRestoreIdentityCount, TargetSeq: 3, TargetShard: 0, TargetHashVersion: 3,
		TargetRowHash: randomBytes(t, partRestoreHashBytes), SourceSeq: 4, SourceShard: 0, SourceRowHash: randomBytes(t, partRestoreHashBytes),
	}
}

func partRestoreManifestMembers(t *testing.T, manifest ops.AuditPartRestoreManifest) map[string]json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode the manifest: %v", err)
	}
	members := map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &members); err != nil {
		t.Fatalf("decode the manifest members: %v", err)
	}
	return members
}

func (p *partRestorePipeline) resultOfManifest(t *testing.T, manifestDigest, targetFlag string) audit.Row {
	t.Helper()

	var operationID uuid.UUID
	rows := p.waitRows(t, audit.SystemOrgID(), audit.VerbOpsBackfillRestoreManifestPart, func(rows []audit.Row) bool {
		for _, intent := range operationRows(rows, audit.PhaseIntent) {
			var extra struct {
				Input struct {
					ManifestSHA256 string `json:"manifest_sha256"`
					TargetEventID  string `json:"target_event_id"`
				} `json:"input"`
			}
			if json.Unmarshal(intent.Extra, &extra) == nil && extra.Input.ManifestSHA256 == manifestDigest && extra.Input.TargetEventID == targetFlag {
				operationID = intent.Context.OperationID
			}
		}
		for _, result := range operationRows(rows, audit.PhaseResult) {
			if result.Context.OperationID == operationID && operationID != uuid.Nil {
				return true
			}
		}
		return false
	})
	for _, result := range operationRows(rows, audit.PhaseResult) {
		if result.Context.OperationID == operationID {
			return result
		}
	}
	t.Fatalf("no result row for manifest %s", manifestDigest)
	return audit.Row{}
}

func (p *partRestorePipeline) requireManifestRefused(t *testing.T, members map[string]json.RawMessage, code, fragment string) {
	t.Helper()
	p.requireRefusedRun(t, members, p.target.String(), p.partURI(), code, fragment)
}

func (p *partRestorePipeline) requireRefusedRun(t *testing.T, members map[string]json.RawMessage, targetFlag, wantTargetURI, code, fragment string) {
	t.Helper()
	content, err := json.Marshal(members)
	if err != nil {
		t.Fatalf("encode the manifest members: %v", err)
	}
	path, digest := p.writeManifestFile(t, content)

	run := p.runManifest(t, true, path, digest, targetFlag)
	if run.err == nil {
		t.Fatalf("the command accepted the manifest, want a refusal with code %s", code)
	}
	result := p.resultOfManifest(t, digest, targetFlag)
	if result.Outcome != audit.OutcomeRefused || result.Error == nil || result.Error.Code != code || !strings.Contains(result.Error.Message, fragment) {
		t.Fatalf("result outcome %q, error %+v, want refused with code %s containing %q", result.Outcome, result.Error, code, fragment)
	}

	if result.Context.Target == nil || result.Context.Target.URI != wantTargetURI {
		t.Fatalf("result target = %+v, want %s", result.Context.Target, wantTargetURI)
	}
	if parts := p.partCount(t); parts != 0 {
		t.Fatalf("the refused command left %d part rows, want 0", parts)
	}
	if changes := p.queryRows(t, p.org, audit.VerbAuditEventPartCreate); len(changes) != 0 {
		t.Fatalf("the refused command recorded %d change events, want 0", len(changes))
	}
}

func TestAuditPartRestoreManifestMembers(t *testing.T) {
	pipeline := startPartRestorePipeline(t)
	for _, name := range partRestoreMandatoryMembers {
		t.Run(name+" omitted", func(t *testing.T) {
			members := partRestoreManifestMembers(t, pipeline.manifest)
			delete(members, name)
			pipeline.requireManifestRefused(t, members, partRestoreInvalidInputCode, name)
		})
		t.Run(name+" null", func(t *testing.T) {
			members := partRestoreManifestMembers(t, pipeline.manifest)
			members[name] = json.RawMessage("null")
			pipeline.requireManifestRefused(t, members, partRestoreInvalidInputCode, name)
		})
	}

	for _, name := range []string{"target_seq", "source_seq", "target_hash_version"} {
		t.Run(name+" zero", func(t *testing.T) {
			members := partRestoreManifestMembers(t, pipeline.manifest)
			members[name] = json.RawMessage("0")
			pipeline.requireManifestRefused(t, members, partRestoreInvalidInputCode, name)
		})
	}

	t.Run("target flag differs from the manifest", func(t *testing.T) {
		other := uuid.Must(uuid.NewV7())
		otherURI := "tack:audit-event/" + other.String() + "/part/0"
		pipeline.requireRefusedRun(t, partRestoreManifestMembers(t, pipeline.manifest), other.String(), otherURI, partRestoreInvalidInputCode, "differs from --target-event-id")
	})
	t.Run("target flag is not a UUID", func(t *testing.T) {
		pipeline.requireRefusedRun(t, partRestoreManifestMembers(t, pipeline.manifest), "not-a-uuid", "tack:audit-event/invalid", partRestoreInvalidInputCode, "is not a UUID")
	})
	t.Run("explicit zero part index and shards", func(t *testing.T) {
		synthetic := syntheticPartRestoreManifest(t)

		members := partRestoreManifestMembers(t, synthetic)
		for _, name := range []string{"part_index", "target_shard", "source_shard"} {
			if string(members[name]) != "0" {
				t.Fatalf("synthetic member %s = %s, want an explicit 0", name, members[name])
			}
		}

		syntheticURI := "tack:audit-event/" + synthetic.TargetEventID.String() + "/part/0"
		pipeline.requireRefusedRun(t, members, synthetic.TargetEventID.String(), syntheticURI, partRestoreRefusedCode, "verify the source event")
	})
}
