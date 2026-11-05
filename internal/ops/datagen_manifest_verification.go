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
	"goodkind.io/tack/internal/datagen"
)

const (
	datagenManifestWait = 2 * time.Minute
	datagenManifestPoll = time.Second
)

// verifyDatagenManifest runs the registered command against a generated read.
func verifyDatagenManifest(ctx context.Context, factory *cli.Factory, scaleName string, seed int64, started time.Time) (*audit.ManifestVerification, error) {
	scale, err := datagen.ParseScale(scaleName)
	if err != nil {
		return nil, err
	}
	reader, err := audit.NewReader(ctx, factory.Cfg.AuditReaderDSN)
	if err != nil {
		slog.ErrorContext(ctx, "qa.datagen.manifest_reader_failed", slog.String("err", "the ledger reader did not open"))
		return nil, fmt.Errorf("open the QA manifest reader: check AUDIT_READER_DSN")
	}
	defer reader.Close()
	deadline := time.NewTimer(datagenManifestWait)
	defer deadline.Stop()
	ticker := time.NewTicker(datagenManifestPoll)
	defer ticker.Stop()
	for {
		for _, workspace := range datagen.PlanIdentities(factory.Cfg, seed, scale).Workspaces {
			page, err := reader.QueryPage(ctx, audit.QueryFilter{
				OrgID: workspace.OrgID, Oldest: started, Latest: clock.Now().Add(time.Minute),
				Action: "", ActorID: uuid.Nil, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: datagenCommitOrderRows,
			}, "")
			if err != nil {
				return nil, fmt.Errorf("find a QA read manifest: %w", err)
			}
			for _, row := range page.Rows {
				if row.Context.Read != nil && row.Context.Read.Count > 0 && row.Context.Read.ManifestParts > 0 {
					return runDatagenManifestCommand(ctx, factory, workspace.OrgID, row.EventID)
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("find a QA read manifest: %w", ctx.Err())
		case <-deadline.C:
			return nil, fmt.Errorf("find a QA read manifest: the generated read did not arrive within %s", datagenManifestWait)
		case <-ticker.C:
		}
	}
}

func runDatagenManifestCommand(ctx context.Context, factory *cli.Factory, orgID, eventID uuid.UUID) (*audit.ManifestVerification, error) {
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
	root.SetArgs([]string{"--output", "json", "--execute", "ops", "audit", "manifest-parts", "--org", orgID.String(), "--event-id", eventID.String(), "--verify-payloads"})
	if err := root.Execute(); err != nil {
		slog.ErrorContext(ctx, "qa.datagen.manifest_verify_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("verify the QA read manifest: %w", err)
	}
	var envelope struct {
		Result struct {
			Verification *audit.ManifestVerification `json:"verification"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil || envelope.Result.Verification == nil || !envelope.Result.Verification.PayloadVerified {
		return nil, fmt.Errorf("verify the QA read manifest: the command did not return a verified result")
	}
	return envelope.Result.Verification, nil
}
