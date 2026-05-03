package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	searchadapter "goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
)

// RepairClass identifies one concrete repair strategy.
type RepairClass string

const (
	// RepairClassStrayAliasState repairs raw `state` aliases into canonical
	// `state_id` props using project-scoped workflow resolution.
	RepairClassStrayAliasState RepairClass = "stray_alias_state"
)

// RepairPreviewInput selects one node and repair class for planning.
type RepairPreviewInput struct {
	Class  RepairClass
	NodeID uuid.UUID
}

// RepairApplyInput confirms and applies one previously previewed repair.
type RepairApplyInput struct {
	ActorID           uuid.UUID
	Class             RepairClass
	ConfirmationToken string
	NodeID            uuid.UUID
}

// RepairPreview describes the current repair decision for one node.
type RepairPreview struct {
	Class                    RepairClass
	NodeID                   uuid.UUID
	NodeType                 string
	CurrentUpdatedAt         time.Time
	Summary                  string
	ConfirmationToken        string
	NeedsRepair              bool
	CanApply                 bool
	RawState                 string
	CanonicalStateID         uuid.UUID
	ResolvedRawStateID       uuid.UUID
	ResolvedCanonicalStateID uuid.UUID
	WinnerStateID            uuid.UUID
	WinnerStateName          string
}

// RepairApplyResult returns the applied write plus the preview it matched.
type RepairApplyResult struct {
	Applied bool
	Preview *RepairPreview
	View    *node.NodeView
}

// RepairConsole previews and applies node repair mutations.
type RepairConsole struct {
	nodeTypes    node.TypeRepository
	propertyDefs node.PropertyDefRepository
	reader       node.NodeReader
	updater      *service.NodeService
}

// NewRepairConsole builds the repair core over generic node repositories.
func NewRepairConsole(
	nodes node.NodeRepository,
	reader node.NodeReader,
	nodeTypes node.TypeRepository,
	propertyDefs node.PropertyDefRepository,
	searcher domainsearch.Searcher,
) *RepairConsole {
	if searcher == nil {
		searcher = searchadapter.Noop{}
	}
	return &RepairConsole{
		nodeTypes:    nodeTypes,
		propertyDefs: propertyDefs,
		reader:       reader,
		updater:      service.NewNodeService(nodes, reader, nodeTypes, propertyDefs, nil, nil, searcher),
	}
}

// NewRepairConsoleFromEnv builds the repair core from the shared ops env.
func NewRepairConsoleFromEnv(env *Env) *RepairConsole {
	return NewRepairConsole(
		env.Stores.Nodes,
		env.Stores.Views,
		env.Stores.NodeTypes,
		env.Stores.PropertyDefs,
		newRepairSearcher(env.Cfg),
	)
}

// Preview plans one repair without mutating storage.
func (c *RepairConsole) Preview(ctx context.Context, in RepairPreviewInput) (*RepairPreview, error) {
	if in.NodeID == uuid.Nil {
		return nil, fmt.Errorf("repair preview node_id required: %w", domain.ErrInvalidArgument)
	}
	plan, err := c.plan(ctx, in.Class, in.NodeID)
	if err != nil {
		return nil, err
	}
	return plan.preview, nil
}

// Apply re-plans the repair and writes only when the token still matches.
func (c *RepairConsole) Apply(ctx context.Context, in RepairApplyInput) (*RepairApplyResult, error) {
	if in.ActorID == uuid.Nil {
		return nil, fmt.Errorf("repair apply actor_id required: %w", domain.ErrInvalidArgument)
	}
	if strings.TrimSpace(in.ConfirmationToken) == "" {
		return nil, fmt.Errorf("repair apply confirmation token required: %w", domain.ErrInvalidArgument)
	}
	plan, err := c.plan(ctx, in.Class, in.NodeID)
	if err != nil {
		return nil, err
	}
	if !plan.preview.CanApply {
		return nil, fmt.Errorf("repair %s for node %s is not applicable: %w", in.Class, in.NodeID, domain.ErrFailedPrecondition)
	}
	if in.ConfirmationToken != plan.preview.ConfirmationToken {
		return nil, fmt.Errorf("repair %s confirmation token mismatch for node %s: %w", in.Class, in.NodeID, domain.ErrFailedPrecondition)
	}
	view, err := c.updater.Update(ctx, service.UpdateInput{
		NodeID:  in.NodeID,
		Props:   plan.props,
		ActorID: in.ActorID,
	})
	if err != nil {
		return nil, err
	}
	return &RepairApplyResult{Applied: true, Preview: plan.preview, View: view}, nil
}

type repairPlan struct {
	preview *RepairPreview
	props   map[string]json.RawMessage
}

func (c *RepairConsole) plan(ctx context.Context, class RepairClass, nodeID uuid.UUID) (*repairPlan, error) {
	switch class {
	case RepairClassStrayAliasState:
		return c.planStrayAliasState(ctx, nodeID)
	default:
		return nil, fmt.Errorf("repair class %q: %w", class, domain.ErrInvalidArgument)
	}
}

func newRepairSearcher(cfg *config.Config) domainsearch.Searcher {
	if cfg == nil {
		return searchadapter.Noop{}
	}
	client := searchadapter.New(cfg.MeiliURL, cfg.MeiliMasterKey)
	if err := client.EnsureIndex("nodes", []string{"org_id", "node_type"}); err != nil {
		return searchadapter.Noop{}
	}
	return client
}
