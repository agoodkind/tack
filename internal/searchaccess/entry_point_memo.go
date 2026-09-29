package searchaccess

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// maxMemoPath is the initial capacity of one walk's recorded path.
const maxMemoPath = 8

// entryPointMemoKey identifies the batch memo in a context.
type entryPointMemoKey struct{}

// memoNode identifies one node inside its organization.
type memoNode struct {
	orgID, nodeID uuid.UUID
}

// entryPointMemo records the entry point of every node one bounded batch
// resolved. Each recorded node was read during that batch.
type entryPointMemo struct {
	mutex   sync.Mutex
	entries map[memoNode]uuid.UUID
}

// WithEntryPointMemo returns a context in which entry-point resolution
// records each resolved ancestor. A summary batch uses one memo and reads
// each shared ancestor once.
func WithEntryPointMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, entryPointMemoKey{}, &entryPointMemo{mutex: sync.Mutex{}, entries: map[memoNode]uuid.UUID{}})
}

// memoFrom returns the batch memo of ctx, or nil outside a batch.
func memoFrom(ctx context.Context) *entryPointMemo {
	memo, _ := ctx.Value(entryPointMemoKey{}).(*entryPointMemo)
	return memo
}

// lookup returns the recorded entry point of nodeID in orgID.
func (m *entryPointMemo) lookup(orgID, nodeID uuid.UUID) (uuid.UUID, bool) {
	if m == nil {
		return uuid.Nil, false
	}
	m.mutex.Lock()
	defer m.mutex.Unlock()
	entryPointID, found := m.entries[memoNode{orgID: orgID, nodeID: nodeID}]
	return entryPointID, found
}

// store records entryPointID for every node on path.
func (m *entryPointMemo) store(orgID uuid.UUID, path []uuid.UUID, entryPointID uuid.UUID) {
	if m == nil {
		return
	}
	m.mutex.Lock()
	defer m.mutex.Unlock()
	for _, nodeID := range path {
		m.entries[memoNode{orgID: orgID, nodeID: nodeID}] = entryPointID
	}
}
