package datagen

import (
	"context"
	"encoding/json"
)

const (
	// synonymSetName names the synonym set the generator writes in every
	// workspace, and synonymSetTerms is its terms property.
	synonymSetName  = "Datagen datastore terms"
	synonymSetTerms = `"store, datastore"`
	// synonymIssueName is an issue title that holds the long form of the
	// synonym; the check searches for the short form, which the engine does
	// not match on its own because it is not a prefix of the long form.
	synonymIssueName  = "Datastore datagen synonym check"
	synonymSearchWord = "store"
)

// verifySynonymSearch writes one synonym set and one issue whose title uses
// the long form of the synonym, then requires tack_search to find the issue
// by the short form. It proves the has_synonyms node type, its terms
// property, and the query expansion on a testbed.
func (g *Generator) verifySynonymSearch(ctx context.Context, workspace WorkspaceIdentity, projectIdentifier string) error {
	if g.dryRun {
		return nil
	}
	token := workspace.Actors[0].Token
	existingSets, err := g.driver.Call(ctx, token, "tack_list_synonym_sets", scopeArgs(workspace.Slug, ""))
	if err != nil {
		return loggedError(ctx, "qa datagen: list synonym sets", err)
	}
	_, createdSet, err := g.ensureNode(ctx, token, existingSets, "tack_create_synonym_set", synonymSetName, ToolArguments{
		WorkspaceReference: workspace.Slug,
		Name:               synonymSetName,
		Properties:         NodeProperties{"terms": json.RawMessage(synonymSetTerms)},
	})
	if err != nil {
		return err
	}
	g.recordEnsure(createdSet)
	existingIssues, err := g.driver.Call(ctx, token, "tack_list_issues", scopeArgs(workspace.Slug, projectIdentifier))
	if err != nil {
		return loggedError(ctx, "qa datagen: list issues for the synonym check", err)
	}
	_, createdIssue, err := g.ensureNode(ctx, token, existingIssues, "tack_create_issue", synonymIssueName, ToolArguments{
		WorkspaceReference: workspace.Slug,
		ProjectReference:   projectIdentifier,
		Name:               synonymIssueName,
		Properties:         NodeProperties{},
	})
	if err != nil {
		return err
	}
	g.recordEnsure(createdIssue)
	if createdIssue {
		g.summary.Issues++
	}
	return g.verifySearchFindsIssue(ctx, token, workspace, projectIdentifier, synonymIssueName, synonymSearchWord)
}
