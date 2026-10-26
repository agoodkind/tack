package datagen

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
)

// errSearchMixedSetupStopped reports that the schedule context ended while setup waited for
// search to return the corpus.
var errSearchMixedSetupStopped = errors.New("qa datagen search-mixed: the schedule context ended during setup")

const (
	// searchMixedReadNodes is the number of unchanged issues that the paginated sessions of the
	// first tenant read.
	searchMixedReadNodes = 140
	// searchMixedForeignReadNodes is the number of issues with the same read word in the second
	// tenant.
	searchMixedForeignReadNodes = 10
	// searchMixedVictimSpare is the number of victim issues beyond two full pages.
	searchMixedVictimSpare = 10
	searchMixedVictimPages = 2
)

// searchMixedCorpus stores the IDs of issues that setup creates before measurement starts.
type searchMixedCorpus struct {
	read        []uuid.UUID
	foreignRead []uuid.UUID
	victims     []uuid.UUID
}

// createCorpus creates the read issues of both tenants and the victim issues of the first
// tenant through public calls, then waits until search returns each of them. It records each
// completed wait in the corpus index of the run.
func (m *searchMixed) createCorpus(stopContext, ctx context.Context) error {
	read, err := m.createIssues(ctx, 0, searchMixedReadNodes, func(number int) string {
		return fmt.Sprintf("Read corpus %s issue %d", m.readWord, number)
	})
	if err != nil {
		return err
	}
	foreign, err := m.createIssues(ctx, 1, searchMixedForeignReadNodes, func(number int) string {
		return fmt.Sprintf("Read corpus %s issue %d", m.readWord, number)
	})
	if err != nil {
		return err
	}
	victims, err := m.createIssues(ctx, 0, searchMixedVictimPages*m.limits.MaxResults+searchMixedVictimSpare, func(number int) string {
		return fmt.Sprintf("Victim corpus %s issue %d", m.victimWord, number)
	})
	if err != nil {
		return err
	}
	createsEnded := clock.Now()
	m.corpus = searchMixedCorpus{read: read, foreignRead: foreign, victims: victims}
	waits := []struct {
		group  string
		tenant int
		word   string
		nodes  []uuid.UUID
	}{
		{searchMixedReadGroup, 0, m.readWord, read},
		{searchMixedForeignReadGroup, 1, m.readWord, foreign},
		{searchMixedVictimGroup, 0, m.victimWord, victims},
	}
	for _, wait := range waits {
		run := m.searchRun(wait.tenant, 0, wait.tenant)
		check := func(ctx context.Context) ([]uuid.UUID, error) { return run.missing(ctx, wait.word, wait.nodes...) }
		description := fmt.Sprintf("index search mixed corpus group %s for %s", wait.group, wait.word)
		checks, err := awaitCorpus(stopContext, ctx, description, wait.nodes, check)
		if err != nil {
			return err
		}
		m.corpusIndex.record(SearchMixedCorpusWait{
			Group: wait.group, Tenant: wait.tenant, Nodes: len(wait.nodes), Checks: checks, Elapsed: clock.Now().Sub(createsEnded),
		})
	}
	return nil
}

// searchRun returns a verification run for one tenant actor through one driver.
func (m *searchMixed) searchRun(tenant, actor, driver int) searchRun {
	workspace := m.tenants[tenant].workspace
	return searchRun{
		driver: m.driver(driver), token: workspace.Actors[actor].Token, entry: workspace.Slug,
		limits: m.limits, fixture: searchFixture{}, phrases: searchPhrases{}, pages: m.pages,
	}
}

// createIssues creates count issues in a tenant project through public calls.
// It uses at most searchMixedCreateWorkers concurrent workers and returns IDs in input order.
func (m *searchMixed) createIssues(ctx context.Context, tenant, count int, description func(int) string) ([]uuid.UUID, error) {
	created := make([]uuid.UUID, count)
	failures := m.runCreates(ctx, count, func(ctx context.Context, number int) error {
		nodeID, err := m.createIssue(ctx, tenant, number, description(number))
		created[number] = nodeID
		return err
	})
	if len(failures) > 0 {
		return nil, loggedError(ctx, fmt.Sprintf("qa datagen search-mixed: the command could not create %d setup issues", count), failures[0])
	}
	return created, nil
}

// createIssue uses number to select a driver and creates one issue in a tenant project.
func (m *searchMixed) createIssue(ctx context.Context, tenant, number int, text string) (uuid.UUID, error) {
	properties := newProperties()
	properties.setString("description", text)
	owner := m.tenants[tenant]
	result, err := m.driver(number).Call(ctx, owner.token(), "tack_create_issue", ToolArguments{
		WorkspaceReference: owner.entry(), ProjectReference: owner.project, IssueReference: "",
		Name: text, Properties: properties, NodeID: "", Query: "", NodeType: "",
		Direction: "", SourceID: "", RelationType: "", TargetID: "",
	})
	if err != nil {
		return uuid.Nil, err
	}
	nodeID, err := uuid.Parse(result.RawID())
	if err != nil {
		return uuid.Nil, loggedError(ctx, "qa datagen search-mixed: the command could not parse the created issue ID", err)
	}
	return nodeID, nil
}
