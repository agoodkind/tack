package integration_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"
)

const partRestoreInvalidManifestText = "the part restore manifest is not valid"

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
		t.Fatalf("encode the synthetic manifest: %v", err)
	}
	members := map[string]json.RawMessage{}
	if err := json.Unmarshal(encoded, &members); err != nil {
		t.Fatalf("decode the synthetic manifest members: %v", err)
	}
	return members
}

func runPartRestoreManifestOnly(t *testing.T, members map[string]json.RawMessage, execute bool) error {
	t.Helper()
	content, err := json.Marshal(members)
	if err != nil {
		t.Fatalf("encode the manifest members: %v", err)
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "manifest.json")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write the manifest: %v", err)
	}
	digest := sha256.Sum256(content)
	cfg := &config.Config{RepairDir: directory, AuditReaderDSN: manifestUnreachableReaderDSN, AuditOperatorDSN: manifestUnreachableReaderDSN}
	factory := cli.System(cfg)
	factory.Out, factory.Err = &bytes.Buffer{}, &bytes.Buffer{}
	root := nodeAuditCommandRoot(factory)
	root.SetContext(t.Context())
	arguments := []string{
		"--output", "json", "--operator-id", uuid.NewString(), "--operator-email", "manifest-members@example.test",
		"ops", "audit", "backfill", "once-restore-manifest-part", "--manifest", path, "--manifest-sha256", hex.EncodeToString(digest[:]),
	}
	if execute {
		arguments = append([]string{"--execute"}, arguments...)
	}
	root.SetArgs(arguments)
	return root.Execute()
}

func requirePartRestoreManifestRefused(t *testing.T, members map[string]json.RawMessage, fragment string) {
	t.Helper()
	for _, execute := range []bool{false, true} {
		err := runPartRestoreManifestOnly(t, members, execute)
		if err == nil || !strings.Contains(err.Error(), partRestoreInvalidManifestText) || !strings.Contains(err.Error(), fragment) {
			t.Fatalf("manifest run (execute %v) = %v, want a refusal containing %q before any database call", execute, err, fragment)
		}
	}
}

func TestAuditPartRestoreManifestRefusesOmittedAndNullMembers(t *testing.T) {
	for _, name := range partRestoreMandatoryMembers {
		t.Run(name+" omitted", func(t *testing.T) {
			members := partRestoreManifestMembers(t, syntheticPartRestoreManifest(t))
			delete(members, name)
			requirePartRestoreManifestRefused(t, members, name)
		})
		t.Run(name+" null", func(t *testing.T) {
			members := partRestoreManifestMembers(t, syntheticPartRestoreManifest(t))
			members[name] = json.RawMessage("null")
			requirePartRestoreManifestRefused(t, members, name)
		})
	}
}

func TestAuditPartRestoreManifestRefusesNonPositiveValues(t *testing.T) {
	cases := map[string]string{
		"target_seq": "0", "source_seq": "0", "target_hash_version": "0",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			members := partRestoreManifestMembers(t, syntheticPartRestoreManifest(t))
			members[name] = json.RawMessage(value)
			requirePartRestoreManifestRefused(t, members, name)
		})
	}
}

func TestAuditPartRestoreManifestAcceptsExplicitZeroes(t *testing.T) {
	members := partRestoreManifestMembers(t, syntheticPartRestoreManifest(t))
	for _, name := range []string{"part_index", "target_shard", "source_shard"} {
		if string(members[name]) != "0" {
			t.Fatalf("synthetic member %s = %s, want an explicit 0", name, members[name])
		}
	}
	err := runPartRestoreManifestOnly(t, members, true)
	if err == nil || !strings.Contains(err.Error(), "outbox is nil") {
		t.Fatalf("a manifest with explicit zero part_index and shards stopped with %v, want the missing audit outbox that follows manifest validation", err)
	}
	if strings.Contains(err.Error(), partRestoreInvalidManifestText) {
		t.Fatalf("a manifest with explicit zero part_index and shards was refused: %v", err)
	}
}
