package datagen

import "context"

// openRevokedSession reads the first two pages of the member session for the revocation check.
// It adds the unavailable responses that it repeated to the revocation result, including the
// responses of a session that failed to open.
func (m *searchMixed) openRevokedSession(ctx context.Context, session searchMixedSession) (searchPage, searchPage, error) {
	tally := searchMixedTally{}
	defer m.phases.update(func(phases *searchMixedPhaseStats) { phases.revocation.Unavailable += tally.unavailable })
	first, err := m.page(ctx, session, 0, "", &tally)
	if err != nil {
		return searchPage{}, searchPage{}, err
	}
	second, err := m.page(ctx, session, 1, first.Cursor, &tally)
	if err != nil {
		return searchPage{}, searchPage{}, err
	}
	if first.Cursor == "" || second.Cursor == "" {
		return searchPage{}, searchPage{}, loggedError(ctx, "qa datagen search-mixed: the command could not open the member's search session", errSearchMixedNoContinuation)
	}
	return first, second, nil
}
