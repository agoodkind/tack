package datagen

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const (
	// searchContinuationNodes exceeds one public page of 25 nodes.
	searchContinuationNodes = 30
	// searchFillerRepeats makes the final-page value and the duplicate-heavy
	// continuation value each longer than one 4,096-byte reader page.
	searchFillerRepeats = 300
)

// searchCorpus is the node identity of every verification case.
type searchCorpus struct {
	semantic     map[string]uuid.UUID
	finalPage    uuid.UUID
	excluded     uuid.UUID
	edited       uuid.UUID
	deleted      uuid.UUID
	continuation []uuid.UUID
	throughMCP   uuid.UUID
}

// verify creates the corpus, waits for each case, applies an edit and a
// deletion, and waits for the changed results.
func (r searchRun) verify(ctx context.Context, workspace WorkspaceIdentity) error {
	corpus, err := r.createCorpus(ctx, workspace)
	if err != nil {
		return err
	}
	for _, pair := range r.phrases.Semantic {
		nodeID := corpus.semantic[pair.Query]
		if err := r.eventually(ctx, "rank semantic target for "+pair.Query, func(ctx context.Context) error {
			return r.ranked(ctx, pair.Query, nodeID)
		}); err != nil {
			return err
		}
	}
	checks := []struct {
		description string
		check       func(context.Context) error
	}{
		{"find final-page text", func(ctx context.Context) error { return r.ranked(ctx, r.phrases.FinalPage, corpus.finalPage) }},
		{"find node created through MCP", func(ctx context.Context) error {
			return r.includes(ctx, r.phrases.CreatedThroughMCP, corpus.throughMCP)
		}},
		{"find included text of excluded case", func(ctx context.Context) error {
			return r.includes(ctx, r.phrases.ExcludedVisible, corpus.excluded)
		}},
		{"find text before edit", func(ctx context.Context) error { return r.includes(ctx, r.phrases.EditBefore, corpus.edited) }},
		{"find node before deletion", func(ctx context.Context) error { return r.includes(ctx, r.phrases.Deleted, corpus.deleted) }},
		{"traverse every continuation", func(ctx context.Context) error {
			return r.includes(ctx, r.phrases.Continuation, corpus.continuation...)
		}},
	}
	for _, step := range checks {
		if err := r.eventually(ctx, step.description, step.check); err != nil {
			return err
		}
	}
	if err := r.notIndexed(ctx, r.phrases.Excluded, corpus.excluded); err != nil {
		return loggedError(ctx, "qa datagen: index excluded text", err)
	}
	return r.verifyChanges(ctx, corpus)
}

// verifyChanges replaces a long value with a shorter one and deletes a node.
// It then requires search to return the new text and to omit the old text and
// the deleted node.
func (r searchRun) verifyChanges(ctx context.Context, corpus searchCorpus) error {
	if err := r.fixture.edit(ctx, corpus.edited, r.phrases.EditAfter); err != nil {
		return err
	}
	if err := r.fixture.remove(ctx, corpus.deleted); err != nil {
		return err
	}
	changes := []struct {
		description string
		check       func(context.Context) error
	}{
		{"find text after edit", func(ctx context.Context) error { return r.includes(ctx, r.phrases.EditAfter, corpus.edited) }},
		{"drop text before edit", func(ctx context.Context) error { return r.excludes(ctx, r.phrases.EditBefore, corpus.edited) }},
		{"drop deleted node", func(ctx context.Context) error { return r.excludes(ctx, r.phrases.Deleted, corpus.deleted) }},
	}
	for _, step := range changes {
		if err := r.eventually(ctx, step.description, step.check); err != nil {
			return err
		}
	}
	return nil
}

// createCorpus writes every case node and creates one project through the
// public MCP create tool.
func (r searchRun) createCorpus(ctx context.Context, workspace WorkspaceIdentity) (searchCorpus, error) {
	corpus := searchCorpus{semantic: make(map[string]uuid.UUID), continuation: make([]uuid.UUID, 0, searchContinuationNodes+1)}
	var err error
	for _, pair := range r.phrases.Semantic {
		if corpus.semantic[pair.Query], err = r.fixture.put(ctx, pair.Text, pair.Text, ""); err != nil {
			return corpus, err
		}
	}
	for _, distractor := range r.phrases.Distractors {
		if _, err := r.fixture.put(ctx, distractor, distractor, ""); err != nil {
			return corpus, err
		}
	}
	filler := strings.Repeat(r.phrases.Filler, searchFillerRepeats)
	cases := []struct {
		target             *uuid.UUID
		name, value, other string
	}{
		{&corpus.finalPage, "final page case", filler + r.phrases.FinalPage, ""},
		{&corpus.excluded, "excluded case", r.phrases.ExcludedVisible, r.phrases.Excluded},
		{&corpus.edited, "edit case", r.phrases.EditBefore, ""},
		{&corpus.deleted, "deletion case", r.phrases.Deleted, ""},
	}
	for _, item := range cases {
		if *item.target, err = r.fixture.put(ctx, item.name, item.value, item.other); err != nil {
			return corpus, err
		}
	}
	heavy := strings.Repeat(r.phrases.Continuation+" ", searchFillerRepeats)
	for number := range searchContinuationNodes + 1 {
		value := r.phrases.Continuation
		if number == searchContinuationNodes {
			value = heavy
		}
		nodeID, putErr := r.fixture.put(ctx, "continuation case "+strconv.Itoa(number), value, "")
		if putErr != nil {
			return corpus, putErr
		}
		corpus.continuation = append(corpus.continuation, nodeID)
	}
	corpus.throughMCP, err = r.createProject(ctx, workspace)
	return corpus, err
}

// createProject creates one project through tack_create_project and returns
// its node ID.
func (r searchRun) createProject(ctx context.Context, workspace WorkspaceIdentity) (uuid.UUID, error) {
	properties := newProperties()
	properties["identifier"] = json.RawMessage(strconv.Quote(strings.ToUpper(opaqueKey("q"))))
	result, err := r.driver.Call(ctx, r.token, "tack_create_project", ToolArguments{
		WorkspaceReference: workspace.Slug, ProjectReference: "", IssueReference: "",
		Name: r.phrases.CreatedThroughMCP, Properties: properties, NodeID: "", Query: "", NodeType: "",
		Direction: "", SourceID: "", RelationType: "", TargetID: "",
	})
	if err != nil {
		return uuid.Nil, loggedError(ctx, "qa datagen: create search project", err)
	}
	projectID, err := uuid.Parse(result.RawID())
	if err != nil {
		return uuid.Nil, loggedError(ctx, "qa datagen: parse search project id", fmt.Errorf("%q: %w", result.RawID(), err))
	}
	return projectID, nil
}
