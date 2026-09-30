package testenv

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/moby/moby/client"
)

const traefikAccessPath = "/tmp/search-access.json"

// SearchRequests returns successful search requests accepted by each backend.
func (c *OpenSearchCluster) SearchRequests(t T) map[string]int {
	t.Helper()
	counts := map[string]int{}
	c.run(t, func(ctx context.Context, cli *client.Client) error {
		contents, err := readContainerFile(ctx, cli, c.proxy, traefikAccessPath)
		if err != nil {
			slog.ErrorContext(ctx, "testenv.proxy.access_read_failed", slog.String("err", err.Error()))
			return fmt.Errorf("read proxy search access log: %w", err)
		}
		// A file snapshot can include an access record still being appended.
		complete := bytes.LastIndexByte(contents, '\n') + 1
		scanner := bufio.NewScanner(bytes.NewReader(contents[:complete]))
		for scanner.Scan() {
			var record struct {
				ServiceURL string `json:"ServiceURL"`
				Path       string `json:"RequestPath"`
				Status     int    `json:"DownstreamStatus"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
				slog.ErrorContext(ctx, "testenv.proxy.access_decode_failed", slog.String("err", err.Error()))
				return fmt.Errorf("decode proxy access record: %w", err)
			}
			path, _, _ := strings.Cut(record.Path, "?")
			if path == "/_search" && record.Status >= 200 && record.Status < 300 {
				counts[strings.TrimSuffix(record.ServiceURL, "/")]++
			}
		}
		if err := scanner.Err(); err != nil {
			slog.ErrorContext(ctx, "testenv.proxy.access_scan_failed", slog.String("err", err.Error()))
			return fmt.Errorf("scan proxy access log: %w", err)
		}
		return nil
	})
	return counts
}
