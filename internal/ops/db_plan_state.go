package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// dbPlanPrincipalRefusal is the refusal of a statement or a close by a caller
// that dbPlanPrincipal.matches rejects.
const dbPlanPrincipalRefusal = "the caller is a different agent or operator than the plan opener, " +
	"or acts for a different accountable operator; any session of the opener is permitted"

// dbPlanAttempt is one statement run under a plan: its pending row and, when
// the statement finished, its outcome row.
type dbPlanAttempt struct {
	AttemptID    uuid.UUID     `json:"attempt_id"`
	Statement    string        `json:"statement"`
	Outcome      audit.Outcome `json:"outcome"`
	Error        string        `json:"error,omitempty"`
	CommandTag   string        `json:"command_tag,omitempty"`
	RowsReturned int           `json:"rows_returned"`
}

// dbPlanState is a plan as its ledger rows define it.
type dbPlanState struct {
	open     *dbPlanExtra
	closed   bool
	attempts []dbPlanAttempt
}

// newDBPlanState decodes the rows of one plan, oldest first. The first open
// row defines the plan. Only a close row with outcome ok closes the plan; a
// refused close row leaves it open. Statement rows pair by attempt ID, and a
// refused statement is one row with outcome refused.
func newDBPlanState(ctx context.Context, rows []dbPlanRow) (dbPlanState, error) {
	state := dbPlanState{open: nil, closed: false, attempts: nil}
	positions := map[uuid.UUID]int{}
	for _, row := range rows {
		verb := audit.Verb(row.Verb)
		if verb == audit.VerbOpsDBPlanClose {
			state.closed = state.closed || audit.Outcome(row.Outcome) == audit.OutcomeOK
			continue
		}
		if verb == audit.VerbOpsDBPlanOpen {
			if state.open != nil {
				continue
			}
			var extra dbPlanExtra
			if err := decodeDBPlanExtra(ctx, row, &extra); err != nil {
				return state, err
			}
			state.open = &extra
			continue
		}
		var extra dbBreakGlassExtra
		if err := decodeDBPlanExtra(ctx, row, &extra); err != nil {
			return state, err
		}
		position, seen := positions[extra.AttemptID]
		if !seen {
			position = len(state.attempts)
			positions[extra.AttemptID] = position
			state.attempts = append(state.attempts, dbPlanAttempt{
				AttemptID: extra.AttemptID, Statement: extra.Statement, Outcome: audit.OutcomePending,
				Error: "", CommandTag: "", RowsReturned: 0,
			})
		}
		if audit.Outcome(row.Outcome) == audit.OutcomePending {
			continue
		}
		attempt := &state.attempts[position]
		attempt.Outcome, attempt.Error = audit.Outcome(row.Outcome), row.Error
		attempt.CommandTag, attempt.RowsReturned = extra.CommandTag, extra.RowsReturned
	}
	return state, nil
}

// decodeDBPlanExtra decodes the extra payload of row into target.
func decodeDBPlanExtra[T dbPlanExtra | dbBreakGlassExtra](ctx context.Context, row dbPlanRow, target *T) error {
	if err := json.Unmarshal([]byte(row.Extra), target); err != nil {
		slog.ErrorContext(ctx, "db.plan.extra_decode_failed",
			slog.String("event_id", row.EventID), slog.String("err", err.Error()))
		return fmt.Errorf("decode the extra payload of plan row %s: %w", row.EventID, err)
	}
	return nil
}

// refusal returns why the plan does not permit principal to run statement at
// now, or an empty string when it does. The checks run in this order: open
// row, principal, expiry, close row, statement list.
func (s dbPlanState) refusal(principal audit.OperatorPrincipal, statement string, now time.Time) string {
	if s.open == nil {
		return "the ledger has no open row for this plan"
	}
	if !s.open.Principal.matches(principal) {
		return dbPlanPrincipalRefusal
	}
	if s.expired(now) {
		return "the plan expired at " + s.open.ExpiresAt.UTC().Format(backupAlarmTimeLayout)
	}
	if s.closed {
		return "the plan is closed"
	}
	if !slices.Contains(s.open.Statements, statement) {
		return "the plan does not list this statement"
	}
	return ""
}

// expired reports whether now is at or after the plan expiry.
func (s dbPlanState) expired(now time.Time) bool {
	return s.open != nil && !now.Before(s.open.ExpiresAt)
}
