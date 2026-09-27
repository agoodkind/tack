package datagen

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
)

const (
	// searchConvergenceDeadline bounds how long one check waits for workers
	// to index committed changes.
	searchConvergenceDeadline = 5 * time.Minute
	searchPollInterval        = 2 * time.Second
	// searchRankBound is the number of leading distinct nodes in the complete
	// continuation traversal that must contain a relevance target. The search
	// acceptance criteria count these nodes across continuations. A public page
	// can end at its byte limit before it returns 25 nodes.
	searchRankBound = 25
)

// storedPageReader reads the page text that the serving search index stores
// for one node.
type storedPageReader interface {
	SearchPageText(context.Context, uuid.UUID) ([]string, error)
}

// searchRun is one verification session under one entry node.
type searchRun struct {
	driver  *Driver
	token   string
	entry   string
	limits  searchLimits
	fixture searchFixture
	phrases searchPhrases
	pages   storedPageReader
}

// eventually repeats check until it succeeds or the deadline passes.
func (r searchRun) eventually(ctx context.Context, description string, check func(context.Context) error) error {
	deadline := clock.Now().Add(searchConvergenceDeadline)
	for {
		err := check(ctx)
		if err == nil {
			return nil
		}
		if clock.Now().After(deadline) {
			return loggedError(ctx, "qa datagen: "+description, err)
		}
		timer := time.NewTimer(searchPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return loggedError(ctx, "qa datagen: "+description, ctx.Err())
		case <-timer.C:
		}
	}
}

// ranked requires each node to appear within the first searchRankBound
// distinct nodes of the complete traversal for query.
func (r searchRun) ranked(ctx context.Context, query string, nodes ...uuid.UUID) error {
	results, err := traverseSearch(ctx, r.driver, r.token, r.entry, query, r.limits)
	if err != nil {
		return err
	}
	for _, nodeID := range nodes {
		position := slices.Index(results, nodeID)
		if position < 0 || position >= searchRankBound {
			return fmt.Errorf("search %q ranks node %s at %d, want within the first %d distinct nodes", query, nodeID, position, searchRankBound)
		}
	}
	return nil
}

// includes requires each node to appear in the complete traversal for query.
func (r searchRun) includes(ctx context.Context, query string, nodes ...uuid.UUID) error {
	results, err := traverseSearch(ctx, r.driver, r.token, r.entry, query, r.limits)
	if err != nil {
		return err
	}
	for _, nodeID := range nodes {
		if !slices.Contains(results, nodeID) {
			return fmt.Errorf("search %q omitted node %s", query, nodeID)
		}
	}
	return nil
}

// notIndexed requires nodeID to have at least one active page in the serving
// index and requires no active page text to contain value. Search also ranks
// by semantic similarity to other text of the node. A search result therefore
// cannot prove that value was left out of the index.
func (r searchRun) notIndexed(ctx context.Context, value string, nodeID uuid.UUID) error {
	texts, err := r.pages.SearchPageText(ctx, nodeID)
	if err != nil {
		return err
	}
	if len(texts) == 0 {
		return fmt.Errorf("node %s has no active page in the serving index", nodeID)
	}
	for position, text := range texts {
		if strings.Contains(text, value) {
			return fmt.Errorf("active page %d of node %s stores the excluded value %q", position, nodeID, value)
		}
	}
	return nil
}

// excludes requires every node in nodes to be absent from the complete
// traversal for query.
func (r searchRun) excludes(ctx context.Context, query string, nodes ...uuid.UUID) error {
	results, err := traverseSearch(ctx, r.driver, r.token, r.entry, query, r.limits)
	if err != nil {
		return err
	}
	for _, nodeID := range nodes {
		if slices.Contains(results, nodeID) {
			return fmt.Errorf("search %q returned node %s", query, nodeID)
		}
	}
	return nil
}
