package integration_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// parentMovePage represents one active page document for a node in the serving
// index.
type parentMovePage struct {
	ID               string      `json:"-"`
	NodeRevision     string      `json:"node_revision"`
	PageText         *string     `json:"page_text"`
	SearchGeneration json.Number `json:"search_generation"`
	Access           struct {
		Keys []string `json:"keys"`
	} `json:"access"`
}

// TestSearchParentMoveUpdatesAccessOnly creates a leaf under the first entry
// point and indexes it. The test moves the leaf to the second entry point
// through NodeService.Update, changing only parent_id and scope_id. Every page
// must retain its document ID, node revision, and page_text and receive new
// access keys at a higher search generation. The test then updates the leaf's
// included property. Every page must have a new node revision and the new text.
func TestSearchParentMoveUpdatesAccessOnly(t *testing.T) {
	fixture := newParentEdgeFixture(t)
	client := parentMoveClient(t, fixture)
	leaf := fixture.createLeaf(t, fixture.firstEntry, "quartz moved leaf")
	fixture.drain(t)
	before := parentMovePages(t, client, leaf)

	if err := fixture.moveLeaf(t, leaf, fixture.secondEntry); err != nil {
		t.Fatalf("move leaf %s to the second entry point: %v", leaf, err)
	}

	fixture.drain(t)
	moved := parentMovePages(t, client, leaf)
	if len(moved) != len(before) {
		t.Fatalf(
			"the move changed the page count of leaf %s from %d to %d",
			leaf,
			len(before),
			len(moved),
		)
	}

	for position, page := range moved {
		previous := before[position]
		if page.ID != previous.ID || page.NodeRevision != previous.NodeRevision ||
			*page.PageText != *previous.PageText {
			t.Fatalf(
				"page %d of leaf %s changed its document ID, node revision, or page_text",
				position,
				leaf,
			)
		}

		if slices.Equal(page.Access.Keys, previous.Access.Keys) ||
			parentMoveGeneration(t, page) <= parentMoveGeneration(t, previous) {
			t.Fatalf(
				"page %d of leaf %s kept access %v at generation %s",
				position,
				leaf,
				page.Access.Keys,
				page.SearchGeneration,
			)
		}
	}

	update := service.UpdateInput{
		NodeID: leaf,
		Props: map[string]json.RawMessage{
			fixture.kind.included: parentEdgeJSON(t, "quartz rewritten leaf"),
		},
		ActorID: fixture.first.Actors[0].UserID,
	}
	if _, err := fixture.nodes.Update(
		integration.WithTestOperation(t, t.Context()),
		update,
	); err != nil {
		t.Fatalf("update the included property of leaf %s: %v", leaf, err)
	}

	fixture.drain(t)
	var text strings.Builder

	for _, page := range parentMovePages(t, client, leaf) {
		if page.NodeRevision == moved[0].NodeRevision {
			t.Fatalf(
				"page %s of leaf %s kept node revision %s after the content update",
				page.ID,
				leaf,
				page.NodeRevision,
			)
		}

		text.WriteString(*page.PageText)
	}

	if !strings.Contains(text.String(), "quartz rewritten leaf") {
		t.Fatalf(
			"pages of leaf %s after the content update have text %q, want the new text",
			leaf,
			text.String(),
		)
	}
}
