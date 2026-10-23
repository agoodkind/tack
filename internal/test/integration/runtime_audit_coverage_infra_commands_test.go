//go:build runtimeaudit

package integration_test

import (
	"slices"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

func localTarget(cfg *config.Config) { cfg.DatagenAllowTarget = "local" }

func addBackfillCommandCases(cases map[string]coverageCase) {
	for name, verb := range map[string]audit.Verb{
		"once-audit-partition-names": audit.VerbOpsBackfillAuditPartitionNames,
		"once-drop-killtest-schema":  audit.VerbOpsBackfillDropKilltestSchema,
		"once-fixed-definitions":     audit.VerbOpsBackfillFixedDefinitions,
		"once-metadata-name-index":   audit.VerbOpsBackfillMetadataNameIndex,
		"once-orphan-nodes":          audit.VerbOpsBackfillOrphanNodes,
	} {
		cases["command ops backfill "+name] = commandCase(verb, audit.OutcomeOK, fixedCommand("ops", "backfill", name))
	}
}

// addDatagenCommandCases adds data generator cases with their expected audit
// verbs and outcomes. Search cases use dry runs unless the environment enables
// search integration.
func addDatagenCommandCases(cases map[string]coverageCase) {
	cases["command ops qa datagen seed"] = commandCase(audit.VerbOpsDatagenSeed, audit.OutcomeOK, func(t *testing.T, env *coverageEnv) commandInput {
		ledger, brokers, topic := testenv.Ledger(t), testenv.Kafka(t), env.ledger.stack.topic
		return commandInput{
			args: []string{"ops", "qa", "datagen", "seed", "--scale", "small", "--commit"}, outbox: nil,
			configure: func(cfg *config.Config) {
				localTarget(cfg)
				cfg.AuditReaderDSN, cfg.AuditKafkaBrokers, cfg.AuditKafkaTopic = ledger, brokers, topic
			},
		}
	})
	cases["command ops qa datagen soak"] = commandCase(audit.VerbOpsDatagenSoak, audit.OutcomeOK, func(*testing.T, *coverageEnv) commandInput {
		return commandInput{
			args: []string{"ops", "qa", "datagen", "soak", "--duration", "1s", "--max-ops", "1", "--commit"}, outbox: nil, configure: localTarget,
		}
	})
	cases["command ops qa datagen legacy-ledger-row"] = commandCase(audit.VerbOpsDatagenLegacyLedgerRow, audit.OutcomeOK,
		func(t *testing.T, env *coverageEnv) commandInput {
			ledger := testenv.Ledger(t)
			return commandInput{
				args: []string{"ops", "qa", "datagen", "legacy-ledger-row", "--org", uuid.NewString(), "--commit"}, outbox: nil,
				configure: func(cfg *config.Config) {
					localTarget(cfg)
					cfg.AuditWriterDSN = ledger
				},
			}
		})
	for name, committed := range map[string][]string{
		"search":       {"--commit"},
		"search-load":  {"--commit", "--rate", "60", "--duration", "3s", "--concurrency", "2"},
		"search-mixed": {"--commit", "--rate", "60", "--write-rate", "60", "--duration", "5s", "--concurrency", "2"},
	} {
		verb := audit.VerbOpsDatagenSoak
		if name == "search" {
			verb = audit.VerbOpsDatagenSearch
		}
		command := []string{"ops", "qa", "datagen", name}
		cases["command ops qa datagen "+name] = searchCommandCase(
			commandCase(verb, audit.OutcomeOK, func(*testing.T, *coverageEnv) commandInput {
				return commandInput{args: command, outbox: nil, configure: localTarget}
			}),
			commandCase(verb, audit.OutcomeOK, func(*testing.T, *coverageEnv) commandInput {
				return commandInput{args: slices.Concat(command, committed), outbox: nil, configure: localTarget}
			}),
		)
	}
}

// addInfrastructureCommandCases adds database, search, deployment verification,
// and backup bucket cases with their expected audit verbs and outcomes.
func addInfrastructureCommandCases(cases map[string]coverageCase) {
	cases["command ops db sql"] = commandCase(audit.VerbOpsDBBreakGlass, audit.OutcomeOK, func(t *testing.T, _ *coverageEnv) commandInput {
		mail := testenv.Mailpit(t)
		return commandInput{
			args: []string{"ops", "db", "sql", "--statement", "select 1", "--reason", "coverage"}, outbox: nil,
			configure: func(cfg *config.Config) {
				cfg.BackupAlarmEmail, cfg.BackupAlarmMsmtprcPath = "coverage-alarm@example.test", mail.Msmtprc
			},
		}
	})
	cases["command ops search reindex"] = commandCase(audit.VerbOpsSearchReindex, audit.OutcomeCompleted, func(t *testing.T, env *coverageEnv) commandInput {
		if err := env.stores(t).InitializeSearchIndex(integration.WithTestOperation(t, t.Context()), "node-pages-coverage"); err != nil {
			t.Fatalf("Search index initialization failed; the reindex case requires an initialized index: %v", err)
		}
		t.Setenv("OPENSEARCH_SHARDS", "1")
		t.Setenv("OPENSEARCH_ROUTING_SHARDS", "24")
		t.Setenv("OPENSEARCH_REPLICAS", "0")
		return commandInput{args: []string{"ops", "search", "reindex"}, outbox: nil, configure: nil}
	})
	cases["command ops search verify"] = searchCommandCase(
		failingCommandCase(audit.VerbOpsSearchVerify, audit.OutcomeOK, fixedCommand("ops", "search", "verify")),
		commandCase(audit.VerbOpsSearchVerify, audit.OutcomeOK, fixedCommand("ops", "search", "verify")),
	)
	// The rollback refuses an organization without a rollout in progress.
	cases["command ops search access-rollback"] = failingCommandCase(audit.VerbOpsSearchAccessRollback, audit.OutcomeError,
		func(_ *testing.T, env *coverageEnv) commandInput {
			return commandInput{args: []string{"ops", "search", "access-rollback", "--authority", env.server.orgID.String()}, configure: nil, outbox: nil}
		})
	cases["command ops deploy verify"] = coverageCase{
		run: func(t *testing.T, env *coverageEnv) coverageEvidence {
			fixture := integration.StartContainerdDeployFixture(t)
			server, consumer := integration.PushDeployIndexes(t, fixture)
			integration.CreateDeployContainers(t, fixture)
			operatorID := uuid.Must(uuid.NewV7())
			if output, err := integration.VerifyDeployAs(t, fixture, operatorID.String(), server, consumer); err != nil {
				t.Fatalf("Deployment verification failed; the test expects the matching image digests to pass: %v\n%s", err, output)
			}
			return operatorEvidence(operatorID)
		},
		verb: audit.VerbOpsDeployVerify, outcome: audit.OutcomeOK, check: nil,
	}
	cases["command ops backup buckets-init"] = commandCase(audit.VerbOpsBackupBucketsInit, audit.OutcomeCompleted,
		func(t *testing.T, _ *coverageEnv) commandInput {
			bucket := backupBucketConfig(testenv.ObjectStore(t))
			return commandInput{args: []string{"ops", "backup", "buckets-init"}, outbox: nil, configure: func(cfg *config.Config) {
				cfg.BackupS3Endpoint, cfg.BackupS3Region, cfg.BackupS3BucketMain = bucket.BackupS3Endpoint, bucket.BackupS3Region, bucket.BackupS3BucketMain
				cfg.BackupS3AccessKey, cfg.BackupS3SecretKey = bucket.BackupS3AccessKey, bucket.BackupS3SecretKey
			}}
		})
}
