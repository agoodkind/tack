package testenv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	opensearch "github.com/opensearch-project/opensearch-go/v5"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/telemetry"
)

const openSearchCreateIndexBlock = "cluster.blocks.create_index"

var errOpenSearchCreateIndexBlocked = errors.New("OpenSearch fixture blocks index creation")

type openSearchFilesystemCounters struct {
	Nodes map[string]struct {
		FS struct {
			Total map[string]*int64 `json:"total"`
		} `json:"fs"`
	} `json:"nodes"`
}

// CheckOpenSearchDiskReadiness records only block settings, watermarks and
// filesystem counters. A blocked fixture also reports disk-monitor messages.
func CheckOpenSearchDiskReadiness(ctx context.Context, fixture OpenSearchFixture) error {
	ctx, cancel := context.WithTimeout(ctx, openSearchProbeTimeout)
	defer cancel()
	configuration := search.Config{
		Endpoint:       fixture.Endpoint,
		CA:             fixture.CA,
		Username:       fixture.Username,
		Password:       fixture.Password,
		RequestTimeout: openSearchProbeTimeout,
		MaxRetries:     0,
	}
	client, err := opensearch.NewClient(configuration.ClientConfig(nil))
	if err != nil {
		return diskReadinessFailure(ctx, fmt.Errorf("create the disk readiness client: %w", err))
	}

	defer func() { _ = client.Close() }()

	return openSearchDiskReadiness(ctx, client, fixture)
}

func openSearchDiskReadiness(
	ctx context.Context,
	client *opensearch.Client,
	fixture OpenSearchFixture,
) error {
	settings, blocked, err := readOpenSearchDiskSettings(ctx, client)
	if err != nil {
		return err
	}

	var blockCause error
	if string(blocked) == `"true"` || string(blocked) == "true" {
		blockCause = errOpenSearchCreateIndexBlocked
	}

	failFilesystem := func(cause error) error {
		return diskReadinessFailure(
			ctx,
			fmt.Errorf(
				"effective %s=%s; settings=%s; filesystem measurement failed: %w",
				openSearchCreateIndexBlock,
				blocked,
				settings,
				errors.Join(blockCause, cause),
			),
		)
	}
	var filesystem json.RawMessage
	response, err := opensearch.Execute(
		ctx,
		client,
		http.MethodGet,
		&opensearchapi.NodesStatsReq{
			Metric: []string{"fs"},
			Params: &opensearchapi.NodesStatsParams{
				DebugParams: opensearchapi.DebugParams{FilterPath: []string{"nodes.*.fs.total"}},
			},
		},
		&filesystem,
	)
	if err != nil {
		return failFilesystem(
			fmt.Errorf("read the create-index readiness filesystem counters: %w", err),
		)
	}

	if response == nil {
		return failFilesystem(
			errors.New("the create-index readiness filesystem request did not return a response"),
		)
	}

	if response.IsError() {
		return failFilesystem(
			fmt.Errorf(
				"read the create-index readiness filesystem counters: HTTP %d",
				response.StatusCode,
			),
		)
	}

	var counters openSearchFilesystemCounters
	if err := json.Unmarshal(filesystem, &counters); err != nil {
		return failFilesystem(fmt.Errorf("decode filesystem counters: %w", err))
	}

	if len(counters.Nodes) == 0 {
		return failFilesystem(errors.New("the filesystem response did not include any nodes"))
	}

	for nodeID, node := range counters.Nodes {
		for _, member := range []string{"total_in_bytes", "free_in_bytes", "available_in_bytes"} {
			if node.FS.Total[member] == nil {
				return failFilesystem(
					fmt.Errorf("node %s did not report filesystem counter %s", nodeID, member),
				)
			}
		}
	}

	logOpenSearchDiskReadiness(ctx, blocked, settings, filesystem)
	if string(blocked) != `"true"` && string(blocked) != "true" {
		return nil
	}

	logs := filteredOpenSearchDiskLogs(ctx, fixture)

	return diskReadinessFailure(
		ctx,
		fmt.Errorf(
			"%w: effective %s=%s; settings=%s; filesystem=%s; disk monitor log:\n%s",
			errOpenSearchCreateIndexBlocked,
			openSearchCreateIndexBlock,
			blocked,
			settings,
			filesystem,
			logs,
		),
	)
}

func filteredOpenSearchDiskLogs(ctx context.Context, fixture OpenSearchFixture) string {
	cli, err := dockerClient(ctx)
	if err != nil {
		return "disk monitor log unavailable: " + err.Error()
	}

	defer func() { _ = cli.Close() }()
	logs, err := engineLogTail(ctx, cli, fixture.Container)
	if err != nil {
		return "disk monitor log unavailable: " + err.Error()
	}

	var selected []string

	for line := range strings.SplitSeq(logs, "\n") {
		if strings.Contains(line, "DiskThresholdMonitor") {
			selected = append(selected, line)
		}
	}

	if len(selected) == 0 {
		return "the last " + engineLogLines + " engine log lines do not include DiskThresholdMonitor messages"
	}

	return strings.Join(selected, "\n")
}

func diskReadinessFailure(ctx context.Context, err error) error {
	telemetry.L(ctx).ErrorContext(
		ctx,
		"search.fixture_disk_readiness_failed",
		slog.String("err", err.Error()),
	)

	return err
}

func readOpenSearchDiskSettings(
	ctx context.Context,
	client *opensearch.Client,
) (json.RawMessage, json.RawMessage, error) {
	settings, err := opensearch.Execute[opensearchapi.ClusterGetSettingsResp](
		ctx,
		client,
		http.MethodGet,
		&opensearchapi.ClusterGetSettingsReq{
			Params: &opensearchapi.ClusterGetSettingsParams{
				FlatSettings:    new(true),
				IncludeDefaults: new(true),
				FilterPath: []string{
					"*.cluster*create_index*",
					"*.cluster*watermark*",
				},
			},
		},
		nil,
	)
	if err != nil {
		return nil, nil, diskReadinessFailure(
			ctx,
			fmt.Errorf("read the create-index readiness settings: %w", err),
		)
	}

	if settings == nil {
		return nil, nil, diskReadinessFailure(ctx, errDiskReadinessResponseMissing)
	}

	if settings.IsError() {
		return nil, nil, diskReadinessFailure(
			ctx,
			&diskReadinessStatusError{statusCode: settings.StatusCode},
		)
	}

	var effective opensearchapi.ClusterGetSettingsResp
	if err := json.Unmarshal(settings.RawBody(), &effective); err != nil {
		return nil, nil, diskReadinessFailure(
			ctx,
			fmt.Errorf("decode the create-index readiness settings: %w", err),
		)
	}

	blocked := effective.Defaults[openSearchCreateIndexBlock]
	if value, found := effective.Persistent[openSearchCreateIndexBlock]; found {
		blocked = value
	}

	if value, found := effective.Transient[openSearchCreateIndexBlock]; found {
		blocked = value
	}

	if len(blocked) == 0 {
		return nil, nil, diskReadinessFailure(ctx, errDiskReadinessBlockMissing)
	}

	return settings.RawBody(), blocked, nil
}

func logOpenSearchDiskReadiness(
	ctx context.Context,
	blocked json.RawMessage,
	settings json.RawMessage,
	filesystem json.RawMessage,
) {
	telemetry.L(ctx).InfoContext(
		ctx,
		"search.fixture_disk_readiness",
		slog.String("create_index_block", string(blocked)),
		slog.String("settings", string(settings)),
		slog.String("filesystem", string(filesystem)),
	)
}
