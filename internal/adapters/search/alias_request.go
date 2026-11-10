package search

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/telemetry"
)

func (a *Adapter) putIndexAlias(
	ctx context.Context,
	alias string,
	index string,
) (*opensearchapi.IndicesPutAliasResp, error) {
	response, err := a.api.Indices.PutAlias(
		ctx,
		opensearchapi.IndicesPutAliasReq{Name: alias, Indices: []string{index}},
	)
	if err != nil {
		wrapped := fmt.Errorf("set OpenSearch alias %s to %s: %w", alias, index, err)
		log := telemetry.L(ctx)
		log.ErrorContext(
			ctx,
			"search.alias.set_failed",
			slog.String("err", wrapped.Error()),
			slog.String("alias", alias),
			slog.String("index", index),
		)

		return nil, wrapped
	}

	return response, nil
}
