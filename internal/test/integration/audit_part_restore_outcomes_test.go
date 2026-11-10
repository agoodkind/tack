package integration_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"goodkind.io/tack/internal/audit"
)

func (p *partRestorePipeline) installStaleInjection(t *testing.T) {
	t.Helper()
	function := strings.NewReplacer(
		"{{org}}",
		p.org.String(),
		"{{target}}",
		p.target.String(),
		"{{digest}}",
		p.manifest.ManifestSHA256,
		"{{payload_hex}}",
		hex.EncodeToString(p.payload),
	).Replace(
		readTestSQL(t, "part_restore_stale_create.sql"),
	)
	mustExec(t, p.ledger.admin, function)
	mustExec(t, p.ledger.admin, readTestSQL(t, "part_restore_stale_trigger.sql"))
	t.Cleanup(func() {
		cleanup := context.WithoutCancel(t.Context())

		for _, name := range []string{
			"part_restore_stale_drop.sql",
			"part_restore_stale_drop_function.sql",
		} {
			if _, err := p.ledger.admin.Exec(cleanup, readTestSQL(t, name)); err != nil {
				t.Errorf("run %s: %v", name, err)
			}
		}
	})
}

func (p *partRestorePipeline) dryRunRead(
	t *testing.T,
	manifestDigest string,
	targetFlag string,
) audit.Row {
	t.Helper()

	var found audit.Row

	p.waitRows(
		t,
		audit.SystemOrgID(),
		audit.VerbOpsBackfillRestoreManifestPart,
		func(rows []audit.Row) bool {
			for _, row := range rows {
				var extra struct {
					Input struct {
						ManifestSHA256 string `json:"manifest_sha256"`
						TargetEventID  string `json:"target_event_id"`
					} `json:"input"`
				}
				if row.Context.OperationClass == audit.OperationRead && row.Context.Phase == audit.PhaseRead &&
					json.Unmarshal(row.Extra, &extra) == nil &&
					extra.Input.ManifestSHA256 == manifestDigest &&
					extra.Input.TargetEventID == targetFlag {
					found = row
					return true
				}
			}

			return false
		},
	)

	return found
}

func (p *partRestorePipeline) requireDryRunRefused(
	t *testing.T,
	members map[string]json.RawMessage,
	targetFlag string,
	code string,
) {
	t.Helper()

	content, err := json.Marshal(members)
	if err != nil {
		t.Fatalf("encode the manifest members: %v", err)
	}

	path, digest := p.writeManifestFile(t, content)
	if run := p.runManifest(t, false, path, digest, targetFlag); run.err == nil {
		t.Fatalf("the dry run accepted the manifest, want a refusal with code %s", code)
	}

	read := p.dryRunRead(t, digest, targetFlag)
	if read.Outcome != audit.OutcomeRefused || read.Error == nil || read.Error.Code != code {
		t.Fatalf(
			"dry run record outcome %q, error %+v, want refused with code %s",
			read.Outcome,
			read.Error,
			code,
		)
	}

	if parts := p.partCount(t); parts != 0 {
		t.Fatalf("the refused dry run left %d part rows, want 0", parts)
	}
}

func TestAuditPartRestoreDryRunRefusalsKeepTheirClassification(t *testing.T) {
	pipeline := startPartRestorePipeline(t)
	t.Run(
		"manifest refusal",
		func(t *testing.T) {
			members := partRestoreManifestMembers(t, pipeline.manifest)
			delete(members, "schema_version")
			pipeline.requireDryRunRefused(
				t,
				members,
				pipeline.target.String(),
				partRestoreInvalidInputCode,
			)
		},
	)
	t.Run(
		"restore refusal",
		func(t *testing.T) {
			synthetic := syntheticPartRestoreManifest(t)
			pipeline.requireDryRunRefused(
				t,
				partRestoreManifestMembers(t, synthetic),
				synthetic.TargetEventID.String(),
				partRestoreRefusedCode,
			)
		},
	)
}

func TestAuditPartRestoreGuardRefusalAfterInspectIsRefused(t *testing.T) {
	pipeline := startPartRestorePipeline(t)
	pipeline.installStaleInjection(t)
	if run := pipeline.run(t, true); run.err == nil {
		t.Fatal("the command restored a part that appeared after the inspection, want a refusal")
	}

	result := pipeline.resultOfManifest(t, pipeline.fileDigest, pipeline.target.String())
	if result.Outcome != audit.OutcomeRefused || result.Error == nil ||
		result.Error.Code != partRestoreRefusedCode {
		t.Fatalf(
			"operation result outcome %q, error %+v, want refused with code %s",
			result.Outcome,
			result.Error,
			partRestoreRefusedCode,
		)
	}

	steps := pipeline.waitRows(
		t,
		audit.SystemOrgID(),
		audit.VerbOpsBackfillRestoreManifestPart,
		func(rows []audit.Row) bool {
			for _, row := range rows {
				if row.Context.ParentOperationID == result.Context.OperationID &&
					row.Context.Phase == audit.PhaseResult {
					return true
				}
			}

			return false
		},
	)
	for _, row := range steps {
		if row.Context.ParentOperationID == result.Context.OperationID &&
			row.Context.Phase == audit.PhaseResult &&
			(row.Outcome != audit.OutcomeRefused || row.Error == nil || row.Error.Code != partRestoreRefusedCode) {
			t.Fatalf(
				"step result outcome %q, error %+v, want refused with code %s",
				row.Outcome,
				row.Error,
				partRestoreRefusedCode,
			)
		}
	}

	if changes := pipeline.queryRows(t, pipeline.org, audit.VerbAuditEventPartCreate); len(
		changes,
	) != 0 {
		t.Fatalf("the refused command recorded %d change events, want 0", len(changes))
	}
}
