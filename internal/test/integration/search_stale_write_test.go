package integration

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	textGenerationTen      = "page text written at generation ten"
	textGenerationEleven   = "page text written at generation eleven"
	textGenerationThirteen = "page text written at generation thirteen"
)

// TestSearchStaleContentAfterNewerAccess requires a content write below the
// stored generation to return ErrObsoleteWrite and to change nothing, in the
// serving index and in the mirror index.
func TestSearchStaleContentAfterNewerAccess(t *testing.T) {
	engine := newStaleEngine(t)
	cases := []struct {
		name   string
		mirror bool
	}{{name: "serving", mirror: false}, {name: "mirror", mirror: true}}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			serving := engine.newIndex(t, "stale-content-"+testCase.name)
			mirror := ""
			if testCase.mirror {
				mirror = engine.newIndex(t, "stale-content-"+testCase.name+"-copy")
			}
			nodeID := uuid.Must(uuid.NewV7())
			if _, err := engine.adapter.Put(t.Context(), staleContent(staleWork(nodeID, 10, serving, mirror), textGenerationTen, "key-ten")); err != nil {
				t.Fatalf("put generation 10: %v", err)
			}
			if _, err := engine.adapter.UpdateAccess(t.Context(), staleAccess(staleWork(nodeID, 12, serving, mirror), "key-twelve")); err != nil {
				t.Fatalf("update access at generation 12: %v", err)
			}
			written, err := engine.adapter.Put(t.Context(), staleContent(staleWork(nodeID, 11, serving, mirror), textGenerationEleven, "key-eleven"))
			if !errors.Is(err, searchdomain.ErrObsoleteWrite) || written != 0 {
				t.Fatalf("put generation 11 = %d bytes, error %v, want zero bytes and obsolete write", written, err)
			}
			requireStoredPage(t, engine.client, serving, 12, textGenerationTen, "key-twelve")
			if mirror != "" {
				requireStoredPage(t, engine.client, mirror, 12, textGenerationTen, "key-twelve")
			}
		})
	}
}

// TestSearchStaleAccessAfterNewerContent requires an access update below the
// stored generation to return ErrObsoleteWrite and to leave the source
// byte-for-byte unchanged.
func TestSearchStaleAccessAfterNewerContent(t *testing.T) {
	engine := newStaleEngine(t)
	index := engine.newIndex(t, "stale-access")
	nodeID := uuid.Must(uuid.NewV7())
	if _, err := engine.adapter.Put(t.Context(), staleContent(staleWork(nodeID, 12, index, ""), textGenerationTen, "key-twelve")); err != nil {
		t.Fatalf("put generation 12: %v", err)
	}
	before := rawStaleSource(t, engine.client, index)
	_, err := engine.adapter.UpdateAccess(t.Context(), staleAccess(staleWork(nodeID, 11, index, ""), "key-eleven"))
	if !errors.Is(err, searchdomain.ErrObsoleteWrite) {
		t.Fatalf("access update at generation 11 = %v, want obsolete write", err)
	}
	requireUnchanged(t, engine.client, index, before, "stale access update")
}

// TestSearchCurrentGenerationRetry requires a repeated content write or access
// update at the stored generation to succeed without changing the source.
func TestSearchCurrentGenerationRetry(t *testing.T) {
	engine := newStaleEngine(t)
	index := engine.newIndex(t, "stale-retry")
	nodeID := uuid.Must(uuid.NewV7())
	work := staleWork(nodeID, 12, index, "")
	content := staleContent(work, textGenerationTen, "key-twelve")
	if _, err := engine.adapter.Put(t.Context(), content); err != nil {
		t.Fatalf("put generation 12: %v", err)
	}
	before := rawStaleSource(t, engine.client, index)
	for attempt := range 2 {
		written, err := engine.adapter.Put(t.Context(), content)
		if err != nil || written != 0 {
			t.Fatalf("put retry %d = %d bytes, error %v, want zero bytes and nil", attempt, written, err)
		}
		requireUnchanged(t, engine.client, index, before, "content retry")
	}
	for attempt := range 2 {
		if _, err := engine.adapter.UpdateAccess(t.Context(), staleAccess(work, "key-retry")); err != nil {
			t.Fatalf("access retry %d: %v", attempt, err)
		}
		requireUnchanged(t, engine.client, index, before, "access retry")
	}
}

// TestSearchReadToIndexRace requires a write that changes the page between the
// adapter read and its index request to cause a 409 and a reread. A local TLS
// proxy to the engine runs an access update before it forwards the first bulk
// request.
func TestSearchReadToIndexRace(t *testing.T) {
	engine := newStaleEngine(t)
	cases := []struct {
		name         string
		generation   int64
		text         string
		wantObsolete bool
		wantStored   int64
		wantText     string
	}{
		{name: "injected access is newer", generation: 11, text: textGenerationEleven, wantObsolete: true, wantStored: 12, wantText: textGenerationTen},
		{name: "injected access is older", generation: 13, text: textGenerationThirteen, wantObsolete: false, wantStored: 13, wantText: textGenerationThirteen},
	}
	for position, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			index := engine.newIndex(t, "stale-race-"+string(rune('a'+position)))
			nodeID := uuid.Must(uuid.NewV7())
			if _, err := engine.adapter.Put(t.Context(), staleContent(staleWork(nodeID, 10, index, ""), textGenerationTen, "key-ten")); err != nil {
				t.Fatalf("put generation 10: %v", err)
			}
			inject := func() error {
				_, err := engine.adapter.UpdateAccess(t.Context(), staleAccess(staleWork(nodeID, 12, index, ""), "key-twelve"))
				return err
			}
			proxied, proxy := newStaleProxyAdapter(t, engine.fixture, inject)
			_, err := proxied.Put(t.Context(), staleContent(staleWork(nodeID, testCase.generation, index, ""), testCase.text, "key-put"))
			if testCase.wantObsolete != errors.Is(err, searchdomain.ErrObsoleteWrite) || (!testCase.wantObsolete && err != nil) {
				t.Fatalf("put generation %d = %v, want obsolete %t", testCase.generation, err, testCase.wantObsolete)
			}
			accessKey := "key-twelve"
			if !testCase.wantObsolete {
				accessKey = "key-put"
			}
			requireStoredPage(t, engine.client, index, testCase.wantStored, testCase.wantText, accessKey)
			mgetRequests, bulkRequests := proxy.counts()
			wantBulk := 2
			if testCase.wantObsolete {
				wantBulk = 1
			}
			if mgetRequests != 2 || bulkRequests != wantBulk {
				t.Fatalf("proxy saw %d mget and %d bulk requests, want 2 and %d", mgetRequests, bulkRequests, wantBulk)
			}
		})
	}
}

// TestSearchRetirementOrdering requires a retirement below the stored
// generation to leave the page unchanged, a newer retirement to replace it,
// and a later content write below the retirement to return ErrObsoleteWrite.
func TestSearchRetirementOrdering(t *testing.T) {
	engine := newStaleEngine(t)
	index := engine.newIndex(t, "stale-retire")
	nodeID := uuid.Must(uuid.NewV7())
	if _, err := engine.adapter.Put(t.Context(), staleContent(staleWork(nodeID, 14, index, ""), textGenerationTen, "key-fourteen")); err != nil {
		t.Fatalf("put generation 14: %v", err)
	}
	before := rawStaleSource(t, engine.client, index)
	accepted, err := engine.adapter.Retire(t.Context(), staleRetirement(staleWork(nodeID, 13, index, "")))
	if err != nil || accepted != 1 {
		t.Fatalf("retire at generation 13 = %d accepted, error %v, want 1 and nil", accepted, err)
	}
	requireUnchanged(t, engine.client, index, before, "retirement below the stored generation")
	accepted, err = engine.adapter.Retire(t.Context(), staleRetirement(staleWork(nodeID, 15, index, "")))
	if err != nil || accepted != 1 {
		t.Fatalf("retire at generation 15 = %d accepted, error %v, want 1 and nil", accepted, err)
	}
	var retired struct {
		Retired          bool   `json:"retired"`
		SearchGeneration int64  `json:"search_generation"`
		PageText         string `json:"page_text"`
	}
	if err := json.Unmarshal(rawStaleSource(t, engine.client, index), &retired); err != nil {
		t.Fatalf("decode the retired page: %v", err)
	}
	if !retired.Retired || retired.SearchGeneration != 15 || retired.PageText != "" {
		t.Fatalf("retired page = %+v, want retired at generation 15 without text", retired)
	}
	_, err = engine.adapter.Put(t.Context(), staleContent(staleWork(nodeID, 14, index, ""), textGenerationTen, "key-fourteen"))
	if !errors.Is(err, searchdomain.ErrObsoleteWrite) {
		t.Fatalf("put generation 14 after retirement = %v, want obsolete write", err)
	}
}
