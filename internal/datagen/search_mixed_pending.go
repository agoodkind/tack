package datagen

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"
)

var errSearchMixedNoVictim = errors.New("qa datagen search-mixed: the victim session did not return a victim on its second page or leave one for later pages")

// checkPendingDeletion reads two pages of a victim word session in the first tenant.
// It deletes one victim from the second page and one victim that neither page returned.
// It records whether the serving index still stores an active page for either victim.
// It then replays the second page, reads the remaining pages, and starts a new victim word session.
func (m *searchMixed) checkPendingDeletion(ctx context.Context) error {
	session := m.sessionFor(0, 0, m.victimWord, 0)
	tally := searchMixedTally{}
	first, err := m.page(ctx, session, 0, "", &tally)
	if err != nil {
		return err
	}
	second, err := m.page(ctx, session, 1, first.Cursor, &tally)
	if err != nil {
		return err
	}
	returned, unreturned := m.pickVictims(first, second)
	if returned == uuid.Nil || unreturned == uuid.Nil || first.Cursor == "" || second.Cursor == "" {
		return loggedError(ctx, "qa datagen search-mixed: the command could not select issues for the deletion check", errSearchMixedNoVictim)
	}
	for _, victim := range []uuid.UUID{returned, unreturned} {
		if err := m.deleteNode(ctx, 0, 0, victim); err != nil {
			return err
		}
	}
	pending := m.indexed(ctx, returned) || m.indexed(ctx, unreturned)
	if err := m.replay(ctx, session, 2, first.Cursor, second, &tally); err != nil {
		return err
	}
	if _, err := m.traverse(ctx, session, 3, second.Cursor, &tally); err != nil {
		return err
	}
	if _, err := m.traverse(ctx, m.sessionFor(0, 0, m.victimWord, 1), 0, "", &tally); err != nil {
		return err
	}
	sessionTally := tally
	sessionTally.deleted = 0
	m.recordSessions(sessionTally)
	m.phases.update(func(phases *searchMixedPhaseStats) {
		phases.pending.Checks++
		if pending {
			phases.pending.PendingObserved++
		}
		phases.pending.DeletedNodesReturned += tally.deleted
	})
	return nil
}

func (m *searchMixed) pickVictims(first, second searchPage) (uuid.UUID, uuid.UUID) {
	returned, unreturned := uuid.Nil, uuid.Nil
	for _, nodeID := range second.IDs {
		if slices.Contains(m.corpus.victims, nodeID) {
			returned = nodeID
			break
		}
	}
	for _, nodeID := range m.corpus.victims {
		if !slices.Contains(first.IDs, nodeID) && !slices.Contains(second.IDs, nodeID) {
			unreturned = nodeID
			break
		}
	}
	return returned, unreturned
}

func (m *searchMixed) indexed(ctx context.Context, nodeID uuid.UUID) bool {
	texts, err := m.pages.SearchPageText(ctx, nodeID)
	return err == nil && len(texts) > 0
}
