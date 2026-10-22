package ops

import (
	"context"
	"encoding/json"
	"io"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
)

// stalenessReading does not record metric detail text, which can include
// object store or cluster error messages.
type stalenessReading struct {
	Name             string `json:"name"`
	Verdict          string `json:"verdict"`
	AgeSeconds       *int64 `json:"age_seconds,omitempty"`
	ThresholdSeconds int64  `json:"threshold_seconds"`
	Unknown          string `json:"unknown,omitempty"`
}

type stalenessEvidence struct {
	Bucket   string             `json:"bucket"`
	FDBLeg   string             `json:"fdb_leg"`
	Readings []stalenessReading `json:"readings,omitempty"`
	Stale    []string           `json:"stale,omitempty"`
}

func (stalenessEvidence) isStepEvidence() {}

func runBackupStalenessCheckRecorded(
	ctx context.Context,
	cfg *config.Config,
	out io.Writer,
	steps audit.StepRecorder,
) (audit.OutcomeDetails, error, error) {
	if err := requireBackupStalenessConfig(ctx, cfg); err != nil {
		return commandOutcome(audit.OutcomeRefused, commandConfigCode, err), nil, err
	}
	planned := stalenessEvidence{Bucket: cfg.BackupS3BucketMain, FDBLeg: "run", Readings: nil, Stale: nil}
	if !cfg.BackupFDBContinuous {
		planned.FDBLeg = "skipped"
	}
	plannedJSON, err := encodeEvidence(ctx, planned)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), nil, err
	}
	var metrics []backupStalenessMetric
	var writeErr error
	step := audit.ExternalStep(externalTarget("s3/"+planned.Bucket+"/"+backupStatusPrefix), plannedJSON)
	details, err := recordedStep(ctx, steps, step,
		func(ctx context.Context) error {
			metrics, writeErr = measureBackupStaleness(ctx, cfg, out)
			return nil
		},
		func(ctx context.Context) (json.RawMessage, error) {
			observed := planned
			observed.Readings = stalenessReadings(metrics)
			observed.Stale = staleBackupStalenessMetrics(metrics)
			return encodeEvidence(ctx, observed)
		})
	if err != nil {
		return details, nil, err
	}
	return details, backupStalenessVerdict(ctx, metrics, writeErr), nil
}

func stalenessReadings(metrics []backupStalenessMetric) []stalenessReading {
	readings := make([]stalenessReading, 0, len(metrics))
	for _, metric := range metrics {
		reading := stalenessReading{
			Name: metric.Name, Verdict: metric.verdict(), AgeSeconds: nil,
			ThresholdSeconds: int64(metric.Threshold.Seconds()), Unknown: stalenessCauseName(metric.Unknown),
		}
		if metric.AgeKnown {
			age := int64(metric.Age.Seconds())
			reading.AgeSeconds = &age
		}
		readings = append(readings, reading)
	}
	return readings
}

func stalenessCauseName(cause backupStalenessUnknownCause) string {
	switch cause {
	case backupStalenessAgeKnown:
		return ""
	case backupStalenessNeverRecorded:
		return "never_recorded"
	case backupStalenessUnreadable:
		return "unreadable"
	case backupStalenessStoreUnreachable:
		return "store_unreachable"
	case backupStalenessClusterUnseen:
		return "cluster_unseen"
	default:
		return "unknown"
	}
}
