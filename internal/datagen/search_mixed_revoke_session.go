package datagen

import "context"

// openRevokedSession reads the first two pages of the member search session.
// It returns the count of unavailable responses encountered while retrying those page requests,
// even when a request fails or a page does not return a continuation cursor.
func (m *searchMixed) openRevokedSession(ctx context.Context, session searchMixedSession) (searchPage, searchPage, int, error) {
	tally := searchMixedTally{}
	first, err := m.page(ctx, session, 0, "", &tally)
	if err != nil {
		return searchPage{}, searchPage{}, tally.unavailable, err
	}
	second, err := m.page(ctx, session, 1, first.Cursor, &tally)
	if err != nil {
		return searchPage{}, searchPage{}, tally.unavailable, err
	}
	if first.Cursor == "" || second.Cursor == "" {
		return searchPage{}, searchPage{}, tally.unavailable, loggedError(ctx, "qa datagen search-mixed: the command could not open the member's search session", errSearchMixedNoContinuation)
	}
	return first, second, tally.unavailable, nil
}
