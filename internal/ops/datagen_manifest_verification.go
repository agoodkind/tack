package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
)

const (
	datagenManifestWait = 2 * time.Minute
	datagenManifestPoll = time.Second
)

type datagenManifestVerificationReport struct {
	audit.ManifestVerification
	OrgID     uuid.UUID `json:"org_id"`
	EventID   uuid.UUID `json:"event_id"`
	RequestID string    `json:"request_id"`
}

// verifyDatagenManifest runs the registered command against the selected generated read.
func verifyDatagenManifest(
	ctx context.Context,
	factory *cli.Factory,
	orgID uuid.UUID,
	requestID string,
	started time.Time,
) (*datagenManifestVerificationReport, error) {
	if orgID == uuid.Nil || requestID == "" {
		return nil, fmt.Errorf("the QA seed did not provide a read request identity")
	}
	reader, err := audit.NewReader(ctx, factory.Cfg.AuditReaderDSN)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"qa.datagen.manifest_reader_failed",
			slog.String("err", "the ledger reader did not open"),
		)
		return nil, fmt.Errorf("open the QA manifest reader: check AUDIT_READER_DSN")
	}
	defer reader.Close()
	deadline := time.NewTimer(datagenManifestWait)
	defer deadline.Stop()
	ticker := time.NewTicker(datagenManifestPoll)
	defer ticker.Stop()
	for {
		eventID, err := reader.ReadEventByRequest(
			ctx,
			orgID,
			requestID,
			started,
			clock.Now().Add(time.Minute),
		)
		if err != nil {
			return nil, fmt.Errorf("find a QA read manifest: %w", err)
		}
		if eventID != uuid.Nil {
			verification, err := runDatagenManifestCommand(ctx, factory, orgID, eventID)
			if err != nil {
				return nil, err
			}
			return &datagenManifestVerificationReport{
				ManifestVerification: *verification,
				OrgID:                orgID,
				EventID:              eventID,
				RequestID:            requestID,
			}, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("find a QA read manifest: %w", ctx.Err())
		case <-deadline.C:
			return nil, fmt.Errorf(
				"find a QA read manifest: the generated read did not arrive within %s",
				datagenManifestWait,
			)
		case <-ticker.C:
		}
	}
}

func runDatagenManifestCommand(ctx context.Context, factory *cli.Factory, orgID, eventID uuid.UUID) (
	*audit.ManifestVerification,
	error,
) {
	output := &bytes.Buffer{}
	child := cli.System(factory.Cfg)
	child.Out = output
	child.Err = factory.Err
	child.SetOperatorIdentitySource(factory.OperatorIdentitySource())
	child.SetAuditReads(factory.AuditReads())
	child.SetAuditOutbox(factory.AuditOutbox())
	child.SetAuditInfrastructureProbe(factory.AuditInfrastructureProbe())
	registry := clispec.NewRegistry()
	clispec.Register(registry, auditManifestPartsOp(child))
	root := &cobra.Command{Use: "tack", SilenceErrors: true, SilenceUsage: true}
	child.RegisterGlobalFlags(root)
	for _, command := range clispec.RenderCobra(registry, child) {
		root.AddCommand(command)
	}
	root.SetContext(ctx)
	root.SetArgs([]string{
		"--output",
		"json",
		"--execute",
		"ops",
		"audit",
		"manifest-parts",
		"--org",
		orgID.String(),
		"--event-id",
		eventID.String(),
		"--verify-payloads",
	})
	if err := root.Execute(); err != nil {
		slog.ErrorContext(ctx, "qa.datagen.manifest_verify_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("verify the QA read manifest: %w", err)
	}
	var envelope struct {
		Result struct {
			Verification *audit.ManifestVerification `json:"verification"`
		} `json:"result"`
	}
	if err := json.Unmarshal(
		output.Bytes(),
		&envelope,
	); err != nil || envelope.Result.Verification == nil || !envelope.Result.Verification.PayloadVerified {
		return nil, fmt.Errorf(
			"verify the QA read manifest: the command did not return a verified result",
		)
	}
	return envelope.Result.Verification, nil
}
