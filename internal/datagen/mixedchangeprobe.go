package datagen

import (
	"context"
	"slices"
	"strings"
)

// probeMetadata reads each pending issue's active pages from the serving index with
// SearchPageText on each prober pass. A failed read counts in probe_errors and leaves the check
// pending. When any active page text contains the value, it searches the first page under the
// issue's workspace for that value with public tack_search. It removes the check when that
// search returns the issue. A check still pending when probing stops increments metadata_not_found
// and fails Verify.
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

// probeMoves searches for each moved issue's marker under its destination and original workspaces.
// It removes a check when the first page under the destination workspace returns the issue and
// the first page under the original workspace does not.
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

// probeReturns searches for the check marker under workspace entry as the issue's tenant.
// It reports whether the first page returns the issue. It counts failed searches as probe errors.
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
