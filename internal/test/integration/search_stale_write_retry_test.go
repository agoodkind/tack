package integration

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// TestSearchReadToIndexRaceExhaustsRereads requires a content write to return
// ErrConcurrentWrite after three reads when another write changes the page
// before every bulk request. Each injected access update stays below the
// content generation. Every reread plans the write again, and every bulk
// request gets a 409.
func TestSearchReadToIndexRaceExhaustsRereads(t *testing.T) {
	engine := newStaleEngine(t)
	index := engine.newIndex(t, "stale-race-exhausted")
	nodeID := uuid.Must(uuid.NewV7())
	if _, err := engine.adapter.Put(t.Context(), staleContent(staleWork(nodeID, 10, index, ""), textGenerationTen, "key-ten")); err != nil {
		t.Fatalf("put generation 10: %v", err)
	}
	inject := func(bulkNumber int) error {
		generation := int64(10 + bulkNumber)
		_, err := engine.adapter.UpdateAccess(t.Context(), staleAccess(staleWork(nodeID, generation, index, ""), fmt.Sprintf("key-%d", generation)))
		return err
	}
	proxied, proxy := newStaleProxyAdapter(t, engine.fixture, inject)
	_, err := proxied.Put(t.Context(), staleContent(staleWork(nodeID, 20, index, ""), textGenerationThirteen, "key-twenty"))
	if !errors.Is(err, searchdomain.ErrConcurrentWrite) || errors.Is(err, searchdomain.ErrObsoleteWrite) {
		t.Fatalf("put generation 20 = %v, want ErrConcurrentWrite and not ErrObsoleteWrite", err)
	}
	mgetRequests, bulkRequests := proxy.counts()
	if mgetRequests != 3 || bulkRequests != 3 {
		t.Fatalf("proxy saw %d mget and %d bulk requests, want 3 and 3", mgetRequests, bulkRequests)
	}
	requireStoredPage(t, engine.client, index, 13, textGenerationTen, "key-13")
}
