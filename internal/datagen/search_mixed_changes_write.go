package datagen

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// metadataValues returns a new marker for each excluded definition name when
// tenant is zero and excluded definitions exist. Otherwise, it returns nil.
// Each scheduled create in tenant zero stores each marker under its definition
// name.
func (m *searchMixed) metadataValues(tenant int) map[string]string {
	if tenant != 0 || len(m.changes.definitions) == 0 {
		return nil
	}
	values := make(map[string]string, len(m.changes.definitions))
	for _, definition := range m.changes.definitions {
		values[definition.Name] = newSearchMixedMarker()
	}
	return values
}

// changeMetadata selects an issue that the prober has found for its issue
// marker and that has a value under the next excluded definition. It removes
// the issue from later write selection and increments the used definition
// count.
//
// It reads the issue's active pages from the serving index with SearchPageText
// and records the result:
//
//   - A failed read increments probe_errors and metadata_unchecked_before.
//   - A successful read without active pages increments
//     metadata_unchecked_before.
//   - Active pages with the value increment metadata_present_before; active
//     pages without the value increment metadata_absent_before.
//
// It then stores the definition with Search.Include true. That write requests
// a rescan of issues with values under the definition name. A successful write
// adds a value check and increments the committed metadata change count. A
// failed write increments writes.failed and returns true to use the scheduled
// write index without incrementing the committed count. The function returns
// false if it cannot find a settled issue with a value under the definition.
func (m *searchMixed) changeMetadata(ctx context.Context) bool {
	definition := m.changes.definitions[m.changes.changed]
	pinned, _ := m.pinNode(func(candidate *searchMixedNode) bool {
		return candidate.settled && candidate.metadata[definition.Name] != ""
	})
	if pinned == nil {
		return false
	}
	m.changes.changed++
	check := searchMixedCheck{node: pinned, marker: pinned.metadata[definition.Name]}
	texts, err := m.pages.SearchPageText(ctx, pinned.id)
	switch {
	case err != nil:
		m.stats.count(&m.stats.probeErrors)
		m.stats.count(&m.stats.metadataUncheckedBefore)
	case len(texts) == 0:
		m.stats.count(&m.stats.metadataUncheckedBefore)
	case pageTextContains(texts, check.marker):
		m.stats.count(&m.stats.metadataPresentBefore)
	default:
		m.stats.count(&m.stats.metadataAbsentBefore)
	}
	definition.Search.Include = true
	if err := m.stores.PropertyDefs.Set(ctx, definition); err != nil {
		m.writeFailed(ctx, "qa.datagen.search_mixed_metadata_change_failed", err)
		return true
	}
	m.stats.count(&m.stats.metadataChanges)
	m.probeMutex.Lock()
	defer m.probeMutex.Unlock()
	m.metadataChecks = append(m.metadataChecks, check)
	return true
}

// moveIssue removes an issue from later write selection and sets its
// parent_id and scope_id to its tenant's destination project through
// [service.NodeService.Update]. The update schedules search access work. After
// a successful update, moveIssue removes pending marker checks for the issue
// because searches under the original workspace must omit it. It returns false
// if an issue is not available.
func (m *searchMixed) moveIssue(ctx context.Context) bool {
	pinned, marker := m.pinNode(func(*searchMixedNode) bool { return true })
	if pinned == nil {
		return false
	}
	projectID := m.changes.destinations[pinned.tenant].projectID
	target := json.RawMessage(strconv.Quote(projectID.String()))
	_, err := m.changes.nodes.Update(
		ctx,
		service.UpdateInput{
			NodeID:              pinned.id,
			Name:                nil,
			Props:               map[string]json.RawMessage{"parent_id": target},
			RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil},
			ActorID:             m.tenants[pinned.tenant].workspace.Actors[0].UserID,
			ScopeID:             projectID,
		},
	)
	if err != nil {
		m.writeFailed(ctx, "qa.datagen.search_mixed_move_failed", err)
		return true
	}
	m.stats.count(&m.stats.moves)
	m.probeMutex.Lock()
	defer m.probeMutex.Unlock()
	m.markers = slices.DeleteFunc(
		m.markers,
		func(entry searchMixedMarker) bool { return entry.node == pinned },
	)
	m.moveChecks = append(m.moveChecks, searchMixedCheck{node: pinned, marker: marker})
	return true
}

// probeMetadata reads each pending issue's active serving-index pages with
// SearchPageText on every prober pass. A failed read increments probe_errors
// and leaves the check pending. When active page text contains the value, it
// calls public tack_search for the value under the issue's workspace and
// checks the first page. A returned issue removes the check. A check pending
// when probing stops increments metadata_not_found and fails Verify.
func (m *searchMixed) probeMetadata(ctx context.Context, tick int) {
	m.probeMutex.Lock()
	pending := slices.Clone(m.metadataChecks)
	m.probeMutex.Unlock()
	found := make(map[*searchMixedNode]bool)
	for _, entry := range pending {
		texts, err := m.pages.SearchPageText(ctx, entry.node.id)
		if err != nil {
			m.stats.count(&m.stats.probeErrors)
			continue
		}
		if !pageTextContains(texts, entry.marker) {
			continue
		}
		returned, err := m.probeReturns(ctx, tick, entry, m.tenants[entry.node.tenant].entry())
		if err != nil {
			continue
		}
		found[entry.node] = returned
	}
	m.probeMutex.Lock()
	defer m.probeMutex.Unlock()
	m.metadataChecks = slices.DeleteFunc(
		m.metadataChecks,
		func(entry searchMixedCheck) bool { return found[entry.node] },
	)
}

// pageTextContains reports whether any page text in texts contains value.
func pageTextContains(texts []string, value string) bool {
	for _, text := range texts {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}

// probeMoves searches each moved issue's marker under its destination and
// original workspaces. It removes a check when the destination's first page
// returns the issue and the original workspace's first page omits it.
func (m *searchMixed) probeMoves(ctx context.Context, tick int) {
	m.probeMutex.Lock()
	pending := slices.Clone(m.moveChecks)
	m.probeMutex.Unlock()
	verified := make(map[*searchMixedNode]bool)
	for _, entry := range pending {
		moved, err := m.probeReturns(
			ctx,
			tick,
			entry,
			m.changes.destinations[entry.node.tenant].entry,
		)
		if err != nil || !moved {
			continue
		}
		original, err := m.probeReturns(ctx, tick, entry, m.tenants[entry.node.tenant].entry())
		if err != nil {
			continue
		}
		verified[entry.node] = !original
	}
	m.probeMutex.Lock()
	defer m.probeMutex.Unlock()
	m.moveChecks = slices.DeleteFunc(
		m.moveChecks,
		func(entry searchMixedCheck) bool { return verified[entry.node] },
	)
}

// probeReturns searches the check marker under workspace entry as the issue's
// tenant. It reports whether the first page returns the issue and counts failed
// searches as probe errors.
func (m *searchMixed) probeReturns(
	ctx context.Context,
	tick int,
	check searchMixedCheck,
	entry string,
) (bool, error) {
	tenant := m.tenants[check.node.tenant]
	driver := m.driver(tick + check.node.ordinal)
	identities := m.titleFixture(check.node.tenant).identities(driver, tenant.token(), entry)
	page, err := callSearch(ctx, driver, identities, tenant.token(), entry, check.marker, "")
	if err != nil {
		m.stats.count(&m.stats.probeErrors)
		return false, err
	}
	return slices.Contains(page.IDs, check.node.id), nil
}
