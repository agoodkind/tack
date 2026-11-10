package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/caarlos0/env/v11"
	"goodkind.io/tack/internal/telemetry"
)

// SearchTopology is the operator-selected physical index topology.
type SearchTopology struct {
	Primaries     int `env:"OPENSEARCH_SHARDS,required"`
	RoutingShards int `env:"OPENSEARCH_ROUTING_SHARDS,required"`
	Replicas      int `env:"OPENSEARCH_REPLICAS,required"`
}

// LoadSearchTopology reads the index topology for search provision and
// verification.
//
// LoadSearchTopology rejects missing or invalid environment values, including
// routing shards that are not divisible by primary shards.
//
// The topology requires these settings:
//
//   - OPENSEARCH_SHARDS specifies a positive primary shard count.
//   - OPENSEARCH_ROUTING_SHARDS specifies a positive routing shard count.
//   - OPENSEARCH_REPLICAS specifies a nonnegative replica count.
func LoadSearchTopology(ctx context.Context) (SearchTopology, error) {
	var topology SearchTopology
	if err := env.Parse(&topology); err != nil {
		wrapped := fmt.Errorf("parse search topology: %w", err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.topology.parse_failed",
			slog.String("err", wrapped.Error()),
		)

		return SearchTopology{}, wrapped
	}

	if topology.Primaries <= 0 || topology.RoutingShards <= 0 || topology.Replicas < 0 {
		wrapped := fmt.Errorf(
			"invalid search topology: primaries=%d routing_shards=%d replicas=%d",
			topology.Primaries,
			topology.RoutingShards,
			topology.Replicas,
		)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.topology.invalid",
			slog.String("err", wrapped.Error()),
		)

		return SearchTopology{}, wrapped
	}

	if topology.RoutingShards%topology.Primaries != 0 {
		wrapped := fmt.Errorf(
			"search routing shards %d must be divisible by primaries %d",
			topology.RoutingShards,
			topology.Primaries,
		)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.topology.invalid",
			slog.String("err", wrapped.Error()),
		)

		return SearchTopology{}, wrapped
	}

	return topology, nil
}

// LoadSearchCA reads the trusted certificate bundle at path.
//
// LoadSearchCA treats path as a file path. It does not parse or validate the
// certificates.
//
// The return values distinguish these outcomes:
//
//   - A successful read returns the file contents.
//   - A failed read returns an empty string and the read error with path
//     context.
func LoadSearchCA(ctx context.Context, path string) (string, error) {
	certificate, err := os.ReadFile(path)
	if err != nil {
		wrapped := fmt.Errorf("read OpenSearch CA %q: %w", path, err)
		telemetry.L(ctx).ErrorContext(
			ctx,
			"search.ca.read_failed",
			slog.String("err", wrapped.Error()),
		)

		return "", wrapped
	}

	return string(certificate), nil
}
