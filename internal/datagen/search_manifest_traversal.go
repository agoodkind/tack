package datagen

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
)

func prepareTraversalManifest(ctx context.Context, stores *fdbadapter.Stores, workspace WorkspaceIdentity, manifest *SearchManifest) error {
	phrases, err := loadSearchPhrases(ctx)
	if err != nil {
		return err
	}
	continuation, err := newSearchFixtureWithManifest(ctx, stores, workspace, manifest)
	if err != nil {
		return err
	}
	nodeIDs := make([]uuid.UUID, 0, searchContinuationNodes+1)
	for number := range searchContinuationNodes + 1 {
		value := phrases.Continuation
		if number == searchContinuationNodes {
			value = strings.Repeat(phrases.Continuation+" ", searchFillerRepeats)
		}
		identifier := "continuation-" + strconv.Itoa(number)
		nodeID, err := continuation.putManifestNode(ctx, identifier, identifier, value, manifest)
		if err != nil {
			return err
		}
		nodeIDs = append(nodeIDs, nodeID)
	}
	manifest.Cases = append(manifest.Cases, SearchManifestCase{
		Query: phrases.Continuation, NodeType: continuation.typeKey, ExpectedIDs: nodeIDs, Match: "exact", RankLimit: 0,
	})
	finalPage, err := newSearchFixtureWithManifest(ctx, stores, workspace, manifest)
	if err != nil {
		return err
	}
	nodeID, err := finalPage.putManifestNode(ctx, "final-page", "final page case", strings.Repeat(phrases.Filler, searchFillerRepeats)+phrases.FinalPage, manifest)
	if err != nil {
		return err
	}
	manifest.Cases = append(manifest.Cases, SearchManifestCase{
		Query: phrases.FinalPage, NodeType: finalPage.typeKey, ExpectedIDs: []uuid.UUID{nodeID}, Match: "exact", RankLimit: 0,
	})
	return nil
}

func (fixture searchFixture) putManifestNode(ctx context.Context, identifier, name, text string, manifest *SearchManifest) (uuid.UUID, error) {
	nodeID, err := fixture.put(ctx, name, text, "")
	if err != nil {
		return uuid.Nil, err
	}
	manifest.Nodes = append(manifest.Nodes, SearchManifestNode{
		ID: nodeID, Identifier: identifier, NodeType: fixture.typeKey,
		IncludedKey: fixture.includedKey, ExcludedKey: fixture.excludedKey, TextBytes: len(text), Name: name,
	})
	stored, err := fixture.stores.Nodes.Get(ctx, fixture.orgID, nodeID)
	if err != nil || stored == nil {
		return uuid.Nil, loggedError(ctx, "qa datagen: read prepared node "+nodeID.String(), errMissing(err))
	}
	if stored.Name != name || stored.NodeType != fixture.typeKey || string(stored.Props[fixture.includedKey]) != strconv.Quote(text) {
		return uuid.Nil, fmt.Errorf("qa datagen: prepared node %s has unexpected stored type or text", nodeID)
	}
	return nodeID, nil
}
