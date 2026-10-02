package datagen

import (
	"context"
	"errors"
)

const (
	// soakSearchUnavailableText is the whole tack_search tool error while
	// public search is disabled.
	soakSearchUnavailableText = "Search is temporarily unavailable."
	// soakKindSearch labels the latency of an answered search. A refusal
	// because public search is disabled gets soakKindSearchUnavailable.
	soakKindSearch            = "search"
	soakKindSearchUnavailable = "search_unavailable"
)

// searchProject calls tack_search once under the project workspace for the
// name of one project issue, or for the project reference when the project
// has no issue. It returns the latency kind of the call. A disabled public
// search is counted and does not stop the soak; any other error does.
func (s *Soak) searchProject(
	ctx context.Context,
	project *soakProject,
	actor Actor,
	operationIndex int,
) (string, error) {
	query := project.Reference
	if len(project.Issues) > 0 {
		query = project.Issues[operationIndex/soakOperationKinds%len(project.Issues)].Name
	}
	_, err := s.driver.Call(ctx, actor.Token, "tack_search", ToolArguments{
		WorkspaceReference: project.Workspace.Slug,
		Query:              query,
	})
	var toolError *toolCallError
	if errors.As(err, &toolError) && toolError.message == soakSearchUnavailableText {
		s.summary.SearchesUnavailable++
		return soakKindSearchUnavailable, nil
	}
	if err != nil {
		return soakKindSearch, loggedError(ctx, "qa datagen soak: search "+query, err)
	}
	s.summary.Searches++
	return soakKindSearch, nil
}
