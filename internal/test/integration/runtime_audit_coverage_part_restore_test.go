//go:build runtimeaudit

package integration_test

import (
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

func addPartRestoreCoverageCases(cases map[string]coverageCase) {
	cases["command ops audit backfill once-restore-manifest-part"] = partRestoreCoverageCase()
}

func partRestoreCoverageCase() coverageCase {
	var pipeline *partRestorePipeline
	var output string
	return coverageCase{
		run: func(t *testing.T, env *coverageEnv) coverageEvidence {
			ledger := &nodeAuditLedger{cfg: env.server.config, admin: env.admin, reader: env.ledger.stack.reader}
			pipeline = newPartRestorePipeline(t, ledger)
			input := commandInput{
				args: []string{
					"ops", "audit", "backfill", "once-restore-manifest-part", "--manifest", pipeline.manifestPath,
					"--manifest-sha256", pipeline.fileDigest, "--target-event-id", pipeline.target.String(),
				},
				configure: func(cfg *config.Config) {
					cfg.RepairDir, cfg.AuditOperatorDSN, cfg.AuditReaderDSN = pipeline.repairDir, pipeline.operatorDSN, testenv.Ledger(t)
				},
				outbox: nil,
			}
			operatorID, commandOutput, err := env.runCommand(t, input, "--execute", "--output", "json")
			if err != nil {
				t.Fatalf("The restore command returned an error when the case required success: %v\n%s", err, commandOutput)
			}
			pipeline.operatorID, output = operatorID, commandOutput
			return operatorEvidence(operatorID)
		},
		verb: audit.VerbOpsBackfillRestoreManifestPart, outcome: audit.OutcomeCompleted,
		check: func(t *testing.T, _ *coverageEnv, _ []audit.Row) {
			requestID, traceID := integration.CommandCorrelation(t, output)
			pipeline.requireNoPayloadText(t, output)
			intent, result := pipeline.requireOperationRecords(t, 1)
			pipeline.requireStepRecords(t, intent)
			change := pipeline.requireChangeRecord(t, intent, 1)
			requireRowsCorrelation(t, requestID, traceID, intent, result, change)
		},
	}
}
