package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
)

// dbPlanCommentPrefix starts a comment line in a plan file.
const dbPlanCommentPrefix = "--"

type dbPlanOpenInput struct {
	clispec.InputMarker
	Plan         string
	Reason       string
	ExpiresAfter string
}

// dbPlanOpenResult reports the plan. PlanID is empty on a dry run.
type dbPlanOpenResult struct {
	clispec.ResultMarker
	Command    string    `json:"command"`
	DryRun     bool      `json:"dry_run"`
	PlanID     string    `json:"plan_id,omitempty"`
	SHA256     string    `json:"sha256"`
	Statements []string  `json:"statements"`
	Reason     string    `json:"reason"`
	ExpiresAt  time.Time `json:"expires_at"`
	MailedTo   string    `json:"mailed_to"`
}

// runDBPlanOpen reads the plan file and, with execute, mails the plan to the
// alarm address and then writes the open row. A mail failure returns an
// error before the open row exists, and every statement under that plan ID
// is then refused.
func runDBPlanOpen(ctx context.Context, deps dbSQLDeps, input dbPlanOpenInput, sink clispec.ResultSink, execute bool) error {
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		return errors.New("a reason is required; it is stored on the plan row and mailed to the alarm address")
	}
	if deps.cfg.BackupAlarmEmail == "" {
		return errors.New("TACK_BACKUP_ALARM_EMAIL is empty; a break-glass plan must be mailed before it opens")
	}
	lifetime, err := parseDBPlanLifetime(ctx, input.ExpiresAfter)
	if err != nil {
		return err
	}
	contents, err := os.ReadFile(strings.TrimSpace(input.Plan))
	if err != nil {
		slog.ErrorContext(ctx, "db.plan.file_unreadable", slog.String("err", err.Error()))
		return fmt.Errorf("read the plan file %s: %w", input.Plan, err)
	}
	statements := parseDBPlanStatements(contents)
	if len(statements) == 0 {
		return errors.New("the plan file " + input.Plan + " lists no statement")
	}
	sum := sha256.Sum256(contents)
	result := dbPlanOpenResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.db.plan.open", DryRun: !execute, PlanID: "",
		SHA256: hex.EncodeToString(sum[:]), Statements: statements, Reason: reason,
		ExpiresAt: clock.Now().UTC().Add(lifetime), MailedTo: deps.cfg.BackupAlarmEmail,
	}
	if !execute {
		return writeDBPlanResult(ctx, sink, result)
	}
	principal, err := deps.identity.Resolve(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "db.plan.principal_failed", slog.String("err", err.Error()))
		return fmt.Errorf("resolve the operator for the break-glass plan: %w", err)
	}
	extra := dbPlanExtra{
		PlanID: uuid.Must(uuid.NewV7()), SHA256: result.SHA256, Statements: statements,
		Principal: newDBPlanPrincipal(principal, reason), ExpiresAt: result.ExpiresAt, Reason: reason,
		MailedTo: deps.cfg.BackupAlarmEmail, Postcheck: "", Expired: false,
	}
	if err := mailDBPlanOpened(ctx, deps.cfg, principal, extra); err != nil {
		return err
	}
	if err := recordDBPlan(ctx, deps.outbox, audit.VerbOpsDBPlanOpen, principal, extra, audit.OutcomeOK, nil); err != nil {
		return err
	}
	result.PlanID = extra.PlanID.String()
	return writeDBPlanResult(ctx, sink, result)
}

// parseDBPlanLifetime parses --expires-after as a positive Go duration.
func parseDBPlanLifetime(ctx context.Context, text string) (time.Duration, error) {
	lifetime, err := time.ParseDuration(strings.TrimSpace(text))
	if err != nil {
		slog.ErrorContext(ctx, "db.plan.bad_lifetime", slog.String("err", err.Error()))
		return 0, fmt.Errorf("--expires-after %q is not a Go duration: %w", text, err)
	}
	if lifetime <= 0 {
		return 0, errors.New("--expires-after must be positive, got " + lifetime.String())
	}
	return lifetime, nil
}

// parseDBPlanStatements returns one statement per non-empty line of contents,
// trimmed the way runDBSQL trims --statement. A line that starts with "--"
// is a comment.
func parseDBPlanStatements(contents []byte) []string {
	var statements []string
	for line := range strings.SplitSeq(string(contents), "\n") {
		statement := strings.TrimSpace(line)
		if statement == "" || strings.HasPrefix(statement, dbPlanCommentPrefix) {
			continue
		}
		statements = append(statements, statement)
	}
	return statements
}

// writeDBPlanResult writes a plan command report.
func writeDBPlanResult(ctx context.Context, sink clispec.ResultSink, result clispec.Result) error {
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		slog.ErrorContext(ctx, "db.plan.report_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the break-glass plan report: %w", err)
	}
	return nil
}
