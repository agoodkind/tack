package testenv

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/moby/moby/client"
)

const traefikAccessPath = "/tmp/search-access.json"

// ProxyResponse records the backend and timing of one real proxy response.
type ProxyResponse struct {
	Backend      string `json:"ServiceURL"`
	Path         string `json:"RequestPath"`
	Method       string `json:"RequestMethod"`
	Status       int    `json:"DownstreamStatus"`
	OriginStatus int    `json:"OriginStatus"`
	StartedAt    string `json:"StartUTC"`
	Duration     int64  `json:"Duration"`
}

// SearchRequests returns successful search requests accepted by each backend.
func (c *OpenSearchCluster) SearchRequests(t T) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, record := range c.proxyResponses(t) {
		path, _, _ := strings.Cut(record.Path, "?")
		if path == "/_search" && record.Status >= 200 && record.Status < 300 {
			counts[strings.TrimSuffix(record.Backend, "/")]++
		}
	}
	return counts
}

// PredictionResponses returns prediction responses from complete access records.
func (c *OpenSearchCluster) PredictionResponses(t T, modelID string) []ProxyResponse {
	t.Helper()
	var predictions []ProxyResponse
	for _, record := range c.proxyResponses(t) {
		path, _, _ := strings.Cut(record.Path, "?")
		if path == "/_plugins/_ml/_predict/sparse_encoding/"+modelID {
			predictions = append(predictions, record)
		}
	}
	return predictions
}

func (c *OpenSearchCluster) proxyResponses(t T) []ProxyResponse {
	t.Helper()
	var records []ProxyResponse
	c.run(t, func(ctx context.Context, cli *client.Client) error {
		copied, err := cli.CopyFromContainer(ctx, c.proxy, client.CopyFromContainerOptions{SourcePath: traefikAccessPath})
		if err != nil {
			slog.ErrorContext(ctx, "testenv.proxy.access_read_failed", slog.String("err", err.Error()))
			return fmt.Errorf("read proxy search access log: %w", err)
		}
		defer func() { _ = copied.Content.Close() }()
		archive := tar.NewReader(copied.Content)
		header, err := archive.Next()
		if err != nil {
			slog.ErrorContext(ctx, "testenv.proxy.access_archive_failed", slog.String("err", err.Error()))
			return fmt.Errorf("read proxy access archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			err := fmt.Errorf("proxy access file has archive type %d, want a regular file", header.Typeflag)
			slog.ErrorContext(ctx, "testenv.proxy.access_file_invalid", slog.String("err", err.Error()))
			return err
		}
		reader := bufio.NewReader(archive)
		for {
			line, err := reader.ReadBytes('\n')
			// A file snapshot can end within a record still being appended.
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				slog.ErrorContext(ctx, "testenv.proxy.access_stream_failed", slog.String("err", err.Error()))
				return fmt.Errorf("read proxy access record: %w", err)
			}
			var record ProxyResponse
			if err := json.Unmarshal(line, &record); err != nil {
				slog.ErrorContext(ctx, "testenv.proxy.access_decode_failed", slog.String("err", err.Error()))
				return fmt.Errorf("decode proxy access record: %w", err)
			}
			records = append(records, record)
		}
	})
	return records
}
