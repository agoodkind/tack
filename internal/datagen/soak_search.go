package datagen

import (
	"context"
	"errors"
)

const (
	// soakSearchUnavailableText matches the complete tack_search unavailable
	// error, including refusals when public search is disabled.
	soakSearchUnavailableText = "Search is temporarily unavailable."
	// soakKindSearch identifies the latency of an answered search. An
	// unavailable response, including a refusal when public search is disabled,
	// uses soakKindSearchUnavailable.
	soakKindSearch            = "search"
	soakKindSearchUnavailable = "search_unavailable"
)

// searchProject calls tack_search once in the project workspace with the
// name of a project issue. It uses the project reference when the project
// does not contain an issue. It returns the call's latency kind. It counts
// unavailable responses, including refusals when public search is disabled,
// without stopping the soak. Any other error stops the soak.
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
	if isSearchUnavailable(err) {
		s.summary.SearchesUnavailable++
		return soakKindSearchUnavailable, nil
	}
	if err != nil {
		return soakKindSearch, loggedError(ctx, "qa datagen soak: search "+query, err)
	}
	s.summary.Searches++
	return soakKindSearch, nil
}

// isSearchUnavailable reports whether err is a tack_search tool error that
// exactly matches soakSearchUnavailableText.
func isSearchUnavailable(err error) bool {
	var toolError *toolCallError
	return errors.As(err, &toolError) && toolError.message == soakSearchUnavailableText
}
