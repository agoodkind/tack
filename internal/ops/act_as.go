// act_as.go makes one product write as a named user and leaves two traces:
// a grant row recorded as the operator before the write, and the write's own
// ledger row, which keeps the user as its actor and carries the operator and
// the grant id under the row hash. The grant is recorded first, so a write
// whose grant cannot be recorded does not happen (TACK-424).

package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/service"
)

const (
	// actAsTool is what the user's row records as its tool, so the row says
	// the write came through the act-as command rather than an MCP call.
	actAsTool = "ops_act_as_create"
	// actAsRecordTimeout bounds the grant write, detached from the command's
	// own cancellation the way the other operator detail rows are.
	actAsRecordTimeout = 30 * time.Second
)

type actAsUserFinder interface {
	GetByEmail(ctx context.Context, email string) (*user.User, error)
}

type actAsOrgLister interface {
	ListOrgIDsForUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)
}

type actAsNodeCreator interface {
	Create(ctx context.Context, in service.CreateInput) (*service.CreateResult, error)
}

// actAsDeps is what the command needs, split from the factory so a test can
// hand it fakes and a captured outbox.
type actAsDeps struct {
	outbox   audit.OutboxWriter
	identity audit.OperatorIdentitySource
	users    actAsUserFinder
	members  actAsOrgLister
	reader   referenceRenameResolver
	nodes    actAsNodeCreator
}

// actAsGrantExtra is what the grant row carries beyond the operator: whom the
// operator acted as, where, and why. The user's row names the same grant id.
type actAsGrantExtra struct {
	GrantID      uuid.UUID `json:"grant_id"`
	TargetUserID uuid.UUID `json:"target_user_id"`
	TargetEmail  string    `json:"target_email"`
	OrgID        uuid.UUID `json:"org_id"`
	Reason       string    `json:"reason"`
	NodeType     string    `json:"node_type"`
	ParentID     uuid.UUID `json:"parent_id"`
}

// actAsCreateResult reports what the command did or would do.
type actAsCreateResult struct {
	clispec.ResultMarker
	Command      string `json:"command"`
	DryRun       bool   `json:"dry_run"`
	UserID       string `json:"user_id"`
	Email        string `json:"email"`
	OrgID        string `json:"org_id"`
	ParentID     string `json:"parent_id"`
	NodeType     string `json:"node_type"`
	GrantID      string `json:"grant_id,omitempty"`
	NodeID       string `json:"node_id,omitempty"`
	RowCommitted bool   `json:"row_committed"`
}

// actAsTarget is the resolved user and org the write is made for.
type actAsTarget struct {
	user     *user.User
	orgID    uuid.UUID
	parentID uuid.UUID
}

func runActAsCreate(ctx context.Context, deps actAsDeps, input actAsCreateInput, sink clispec.ResultSink, execute bool) error {
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		return errors.New("a reason is required; it is recorded on the grant and on the user's row")
	}
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.NodeType) == "" {
		return errors.New("--node-type and --name are required")
	}
	target, err := resolveActAsTarget(ctx, deps, input)
	if err != nil {
		return err
	}
	result := actAsCreateResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.act_as.create", DryRun: !execute,
		UserID: target.user.ID.String(), Email: target.user.Email, OrgID: target.orgID.String(),
		ParentID: target.parentID.String(), NodeType: input.NodeType, GrantID: "", NodeID: "", RowCommitted: false,
	}
	if !execute {
		return writeActAsResult(ctx, sink, result)
	}
	principal, err := deps.identity.Resolve(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "act_as.principal_failed", slog.String("err", err.Error()))
		return fmt.Errorf("resolve the operator for the act-as grant: %w", err)
	}
	grant := actAsGrantExtra{
		GrantID: uuid.Must(uuid.NewV7()), TargetUserID: target.user.ID, TargetEmail: target.user.Email,
		OrgID: target.orgID, Reason: reason, NodeType: input.NodeType, ParentID: target.parentID,
	}
	if err := recordActAsGrant(ctx, deps.outbox, principal, grant); err != nil {
		return err
	}
	result.GrantID = grant.GrantID.String()
	created, err := createAsUser(ctx, deps.nodes, target, input, audit.ActProvenance{
		OperatorID: principal.ID, OperatorEmail: principal.Email, GrantID: grant.GrantID, Reason: reason,
	})
	if err != nil {
		return err
	}
	result.NodeID = created.nodeID.String()
	result.RowCommitted = created.rowCommitted
	return writeActAsResult(ctx, sink, result)
}

// resolveActAsTarget names the user and the org the write is for, and refuses
// an unknown user, an unknown parent, or a user outside the parent's org.
func resolveActAsTarget(ctx context.Context, deps actAsDeps, input actAsCreateInput) (actAsTarget, error) {
	none := actAsTarget{user: nil, orgID: uuid.Nil, parentID: uuid.Nil}
	email := strings.TrimSpace(input.Email)
	target, err := deps.users.GetByEmail(ctx, email)
	if errors.Is(err, domain.ErrNotFound) {
		slog.WarnContext(ctx, "act_as.user_unknown", slog.String("email", email))
		return none, fmt.Errorf("no active user %q exists", email)
	}
	if err != nil {
		slog.ErrorContext(ctx, "act_as.user_lookup_failed", slog.String("email", email), slog.String("err", err.Error()))
		return none, fmt.Errorf("look up user %q: %w", email, err)
	}
	parentID, err := uuid.Parse(strings.TrimSpace(input.ParentID))
	if err != nil {
		slog.WarnContext(ctx, "act_as.parent_invalid", slog.String("parent", input.ParentID), slog.String("err", err.Error()))
		return none, fmt.Errorf("--parent must be a UUID: %w", err)
	}
	resolve, err := deps.reader.Resolve(ctx, parentID)
	if err != nil || resolve == nil {
		slog.WarnContext(ctx, "act_as.parent_unknown", slog.String("parent_id", parentID.String()))
		return none, fmt.Errorf("parent %s: %w", parentID, domain.ErrNotFound)
	}
	orgIDs, err := deps.members.ListOrgIDsForUser(ctx, target.ID)
	if err != nil {
		slog.ErrorContext(ctx, "act_as.membership_lookup_failed", slog.String("user_id", target.ID.String()), slog.String("err", err.Error()))
		return none, fmt.Errorf("list orgs for user %s: %w", email, err)
	}
	if !slices.Contains(orgIDs, resolve.OrgID) {
		slog.WarnContext(ctx, "act_as.membership_refused",
			slog.String("user_id", target.ID.String()), slog.String("org_id", resolve.OrgID.String()))
		return none, fmt.Errorf("user %q is not a member of org %s, which holds parent %s", email, resolve.OrgID, parentID)
	}
	return actAsTarget{user: target, orgID: resolve.OrgID, parentID: parentID}, nil
}

func writeActAsResult(ctx context.Context, sink clispec.ResultSink, result actAsCreateResult) error {
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		slog.ErrorContext(ctx, "act_as.report_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the act-as report: %w", err)
	}
	return nil
}
