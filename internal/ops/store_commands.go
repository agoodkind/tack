package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
)

// Store commands run fdbcli and print the cluster's response for operator review.

// errEmptyExcludeList reports an exclusion without usable addresses.
var errEmptyExcludeList = errors.New("no addresses to exclude")

// RunStoreStatus prints the observed cluster status, including process count,
// redundancy mode, coordinator count, and backup agent count.
func RunStoreStatus(ctx context.Context, cfg *config.Config, sink clispec.ResultSink) error {
	output, err := runStoreCLI(ctx, cfg, "status details")
	if err != nil {
		return err
	}

	return writeStoreOutput(ctx, sink, output)
}

// RunStoreSetRedundancy changes the number of copies of each key. On three
// data guests, double tolerates one guest failure; triple requires all three.
func RunStoreSetRedundancy(
	ctx context.Context,
	cfg *config.Config,
	mode string,
	sink clispec.ResultSink,
) error {
	if mode == "" {
		mode = cfg.OpsFDBRedundancyMode
	}

	if err := validateRedundancyMode(mode); err != nil {
		slog.ErrorContext(ctx, "ops.store.redundancy_rejected", slog.String("err", err.Error()))
		return fmt.Errorf("ops store set-redundancy: %w", err)
	}

	slog.InfoContext(ctx, "ops.store.redundancy.start", slog.String("mode", mode))
	output, err := runStoreCLI(ctx, cfg, "configure "+mode+" "+storeStorageEngine)
	if err != nil {
		return err
	}

	slog.InfoContext(ctx, "ops.store.redundancy.done", slog.String("mode", mode))

	return writeStoreOutput(ctx, sink, output)
}

// RunStoreSetCoordinators replaces the coordinator list. The cluster updates
// each connected client's writable cluster file without requiring a restart.
// A read-only file retains the old list; its client loses cluster access when
// the last old coordinator stops.
func RunStoreSetCoordinators(
	ctx context.Context,
	cfg *config.Config,
	list string,
	sink clispec.ResultSink,
) error {
	addresses, err := parseCoordinatorAddresses(list)
	if err != nil {
		slog.ErrorContext(ctx, "ops.store.coordinators_rejected", slog.String("err", err.Error()))
		return fmt.Errorf("ops store set-coordinators: %w", err)
	}

	slog.InfoContext(ctx, "ops.store.coordinators.start", slog.Int("count", len(addresses)))
	output, err := runStoreCLI(ctx, cfg, "coordinators "+strings.Join(addresses, " "))
	if err != nil {
		return err
	}

	slog.InfoContext(ctx, "ops.store.coordinators.done", slog.Int("count", len(addresses)))

	return writeStoreOutput(ctx, sink, output)
}

// RunStoreExclude excludes processes and waits until the cluster replicates
// their keys on other addresses. Stop those processes only after it returns
// successfully to preserve every copy during store relocation.
func RunStoreExclude(
	ctx context.Context,
	cfg *config.Config,
	list string,
	sink clispec.ResultSink,
) error {
	addresses, err := parseExcludeAddresses(list)
	if err != nil {
		slog.ErrorContext(ctx, "ops.store.exclude_rejected", slog.String("err", err.Error()))
		return fmt.Errorf("ops store exclude: %w", err)
	}

	slog.InfoContext(ctx, "ops.store.exclude.start", slog.Int("count", len(addresses)))
	output, err := runStoreCLI(ctx, cfg, "exclude "+strings.Join(addresses, " "))
	if err != nil {
		return err
	}

	slog.InfoContext(ctx, "ops.store.exclude.done", slog.Int("count", len(addresses)))

	return writeStoreOutput(ctx, sink, output)
}

// writeStoreOutput prints the cluster's response and logs and returns output
// failures. Operators require that response before continuing a migration.
func writeStoreOutput(ctx context.Context, sink clispec.ResultSink, output string) error {
	if err := sink.WriteText(ctx, output); err != nil {
		slog.ErrorContext(ctx, "ops.store.write_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the store report: %w", err)
	}

	return nil
}

// parseExcludeAddresses parses process addresses without the coordinator
// command's odd-count requirement. Exclusion counts do not determine quorum.
func parseExcludeAddresses(list string) ([]string, error) {
	fields := strings.Split(list, ",")

	addresses := make([]string, 0, len(fields))
	for _, field := range fields {
		trimmed := strings.TrimSpace(field)
		if trimmed == "" {
			continue
		}

		address, err := normalizeCoordinatorAddress(trimmed)
		if err != nil {
			return nil, err
		}

		addresses = append(addresses, address)
	}

	if len(addresses) == 0 {
		return nil, errEmptyExcludeList
	}

	return addresses, nil
}
