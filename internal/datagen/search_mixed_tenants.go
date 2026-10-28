package datagen

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// searchMixedScale selects the identity scale for bootstrapping two organizations.
// The workload uses the first workspace of each organization.
const searchMixedScale = "medium"

type searchMixedTenant struct {
	workspace WorkspaceIdentity
	project   string
}

func (t *searchMixedTenant) token() string { return t.workspace.Actors[0].Token }

func (t *searchMixedTenant) entry() string { return t.workspace.Slug }

// selectSearchMixedTenants returns the first workspace of each organization.
func selectSearchMixedTenants(identities Identities) []*searchMixedTenant {
	seen := make(map[uuid.UUID]struct{})
	tenants := make([]*searchMixedTenant, 0, len(identities.Workspaces))
	for _, workspace := range identities.Workspaces {
		if _, duplicate := seen[workspace.OrgID]; duplicate {
			continue
		}
		seen[workspace.OrgID] = struct{}{}
		tenants = append(tenants, &searchMixedTenant{workspace: workspace, project: strings.ToUpper(opaqueKey("m"))})
	}
	return tenants
}

func (m *searchMixed) driver(index int) *Driver {
	return m.drivers[index%len(m.drivers)]
}

func (m *searchMixed) createProjects(ctx context.Context) error {
	for index, tenant := range m.tenants {
		properties := newProperties()
		properties.setString("identifier", tenant.project)
		result, err := m.driver(index).Call(ctx, tenant.token(), "tack_create_project", ToolArguments{
			WorkspaceReference: tenant.entry(), ProjectReference: "", IssueReference: "",
			Name: "Search mixed load " + m.runKey, Properties: properties, NodeID: "", Query: "", NodeType: "",
			Direction: "", SourceID: "", RelationType: "", TargetID: "",
		})
		if err != nil {
			return loggedError(ctx, "qa datagen search-mixed: the command could not create a project in "+tenant.entry(), err)
		}
		if err := m.registerCreated(ctx, result); err != nil {
			return err
		}
	}
	return nil
}

// newSearchMixedLoads splits the offered rate and concurrency across the tenants. Every load
// records into one shared statistics object.
func (m *searchMixed) newSearchMixedLoads(ctx context.Context, plannedSearches int) error {
	count := len(m.tenants)
	for index, tenant := range m.tenants {
		rate := share(m.options.Rate, count, index)
		if rate == 0 {
			continue
		}
		planned := int(m.options.Duration * time.Duration(rate) / time.Minute)
		queries, err := newSearchLoadQueries(ctx, m.options.Seed+int64(index), tenant.workspace, max(planned, 1))
		if err != nil {
			return err
		}
		m.loads = append(m.loads, &searchLoad{
			drivers: []*Driver{m.driver(index)}, token: tenant.token(), entry: tenant.entry(),
			options: SearchLoadOptions{
				Seed: m.options.Seed, Rate: rate, Duration: m.options.Duration,
				Concurrency: share(m.options.Concurrency, count, index), Commit: true,
			},
			queries: queries, stats: m.loadStats,
		})
	}
	if len(m.loads) == 0 {
		return fmt.Errorf("qa datagen search-mixed: rate %d does not schedule any searches for %d tenants with %d planned searches", m.options.Rate, count, plannedSearches)
	}
	return nil
}

// share divides total into count parts and returns the part at position.
func share(total, count, position int) int {
	part := total / count
	if position < total%count {
		part++
	}
	return part
}
