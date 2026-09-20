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

// The bodies behind `ops store`. Each one issues one fdbcli command and
// reports what the cluster answered, so an operator running the migration
// reads the same words the cluster printed.

// errEmptyExcludeList is what an exclusion with no usable address returns.
var errEmptyExcludeList = errors.New("no addresses to exclude")

// RunStoreStatus prints the cluster's own status. It is the reading criterion
// 9 is judged from: process count, redundancy mode, coordinator count, and
// backup agent count all come from here rather than from what a deploy
// intended.
func RunStoreStatus(ctx context.Context, cfg *config.Config, sink clispec.ResultSink) error {
	output, err := runStoreCLI(ctx, cfg, "status details")
	if err != nil {
		return err
	}
	return writeStoreOutput(ctx, sink, output)
}

// RunStoreSetRedundancy raises or lowers how many copies of every key the
// cluster keeps. double is the mode three data guests run: it survives one
// guest, where triple needs all three live and would turn one guest's loss
// into an outage.
func RunStoreSetRedundancy(ctx context.Context, cfg *config.Config, mode string, sink clispec.ResultSink) error {
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

// RunStoreSetCoordinators replaces the cluster's coordinator list. The cluster
// writes the new list into every connected client's cluster file itself, so a
// client with a writable file needs no restart. A client that mounts the file
// read-only keeps the old list and stops finding the cluster once the last old
// coordinator stops.
func RunStoreSetCoordinators(ctx context.Context, cfg *config.Config, list string, sink clispec.ResultSink) error {
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

// RunStoreExclude takes a process out of service. The cluster moves every copy
// of a key off the excluded address first and the command returns only once it
// has, so the address can then be stopped without losing a copy. This is how
// the store leaves the guest it started on.
func RunStoreExclude(ctx context.Context, cfg *config.Config, list string, sink clispec.ResultSink) error {
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

// writeStoreOutput prints what the cluster answered. A failed write is logged
// and returned, because an operator running a migration step reads the
// cluster's own words to decide whether to take the next one.
func writeStoreOutput(ctx context.Context, sink clispec.ResultSink, output string) error {
	if err := sink.WriteText(ctx, output); err != nil {
		slog.ErrorContext(ctx, "ops.store.write_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the store report: %w", err)
	}
	return nil
}

// parseExcludeAddresses reads the same address list the coordinator command
// takes, without the odd-count rule: exclusion names the processes that are
// leaving, and that count has no quorum meaning.
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
