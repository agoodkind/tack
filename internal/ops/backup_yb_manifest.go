// backup_yb_manifest.go is the completeness contract between the yb snapshot
// export orchestrator, the per-node archive command, and the restore drill.
// The orchestrator writes a manifest listing every tablet-server node and the
// object-store prefix that node must fill; each data guest's archive command
// uploads its own tablet tar under its prefix; the restore drill refuses a run
// whose manifest lists a node prefix with no archive object behind it.

package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithy "github.com/aws/smithy-go"

	"goodkind.io/tack/internal/telemetry"
)

const (
	// ybSnapshotRootPrefix is the object-store folder that holds one child
	// prefix per export run.
	ybSnapshotRootPrefix = "yugabyte-snapshot/"
	// ybSnapshotManifestObject is the manifest's object base name under the
	// run's key prefix.
	ybSnapshotManifestObject = "manifest.json"
	// ybNodeArchiveObject is the tablet archive's object base name under each
	// node's prefix.
	ybNodeArchiveObject = "tablets.tar.gz"
)

// ybSnapshotManifestNode is one tablet server's slot in the manifest: the node
// name yb-admin reported and the run-relative prefix its archive must land
// under (for example "nodes/yb1/").
type ybSnapshotManifestNode struct {
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
}

// ybSnapshotManifest describes one export run: which snapshot it captured and
// which node prefixes must be filled before the run is restorable.
type ybSnapshotManifest struct {
	RunID      string                   `json:"run_id"`
	SnapshotID string                   `json:"snapshot_id"`
	Database   string                   `json:"database"`
	CreatedAt  string                   `json:"created_at"`
	Nodes      []ybSnapshotManifestNode `json:"nodes"`
}

// newYBSnapshotManifest builds the manifest for a run, one node entry per
// tablet-server name, sorted so the manifest bytes are deterministic for a
// given node set.
func newYBSnapshotManifest(runID, snapshotID, database string, nodeNames []string) ybSnapshotManifest {
	sorted := make([]string, len(nodeNames))
	copy(sorted, nodeNames)
	sort.Strings(sorted)
	nodes := make([]ybSnapshotManifestNode, 0, len(sorted))
	for _, name := range sorted {
		nodes = append(nodes, ybSnapshotManifestNode{Name: name, Prefix: "nodes/" + name + "/"})
	}
	return ybSnapshotManifest{
		RunID:      runID,
		SnapshotID: snapshotID,
		Database:   database,
		CreatedAt:  opsNow().UTC().Format(time.RFC3339),
		Nodes:      nodes,
	}
}

// nodePrefix returns the run-relative prefix the named node must fill, or
// false when the manifest does not list that node.
func (m ybSnapshotManifest) nodePrefix(name string) (string, bool) {
	for _, node := range m.Nodes {
		if node.Name == name {
			return node.Prefix, true
		}
	}
	return "", false
}

// ybNodeArchiveKey is the full object key of one node's tablet archive for the
// manifest's run.
func ybNodeArchiveKey(runID string, node ybSnapshotManifestNode) string {
	return ybSnapshotKeyPrefix(runID) + node.Prefix + ybNodeArchiveObject
}

// missingYBNodeArchives returns the names of the manifest's nodes whose archive
// object is absent, using the caller's existence check so the completeness rule
// stays testable without an object store.
func missingYBNodeArchives(manifest ybSnapshotManifest, exists func(key string) (bool, error)) ([]string, error) {
	var missing []string
	for _, node := range manifest.Nodes {
		present, err := exists(ybNodeArchiveKey(manifest.RunID, node))
		if err != nil {
			return nil, err
		}
		if !present {
			missing = append(missing, node.Name)
		}
	}
	return missing, nil
}

// parseYBTabletServers extracts the tablet-server node names from yb-admin
// list_all_tablet_servers output. Data rows start with the server UUID and
// carry the advertised RPC host:port in the second column; the host is the
// node name the compose deploy announces (never an address, per the identity
// contract in docker-compose.yml). Names are deduplicated and sorted.
func parseYBTabletServers(stdout string) []string {
	seen := map[string]bool{}
	for line := range strings.SplitSeq(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !ybUUIDPattern.MatchString(fields[0]) {
			continue
		}
		seen[hostFromHostPort(fields[1])] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ybFirstMasterHost returns the host of the first entry in a comma-separated
// master_addresses list, for one-shot clients that need any reachable ledger
// node rather than the whole quorum.
func ybFirstMasterHost(masterAddresses string) string {
	first, _, _ := strings.Cut(masterAddresses, ",")
	return hostFromHostPort(strings.TrimSpace(first))
}

// hostFromHostPort strips the port from a host:port pair, unbracketing IPv6
// literals; a value with no port is returned unchanged.
func hostFromHostPort(hostPort string) string {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return strings.Trim(hostPort, "[]")
	}
	return host
}

// fetchYBSnapshotManifest downloads and decodes the manifest of one export
// run. The manifest is small, so it is read into memory rather than staged.
func fetchYBSnapshotManifest(ctx context.Context, client *s3.Client, bucket, runID string) (ybSnapshotManifest, error) {
	logger := telemetry.L(ctx)
	key := ybSnapshotKeyPrefix(runID) + ybSnapshotManifestObject
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		wrapped := fmt.Errorf("get yb snapshot manifest %s/%s: %w", bucket, key, err)
		logger.ErrorContext(ctx, "backup.s3.get_failed", slog.String("err", wrapped.Error()))
		return ybSnapshotManifest{}, wrapped
	}
	defer out.Body.Close()
	body, err := io.ReadAll(out.Body)
	if err != nil {
		wrapped := fmt.Errorf("read yb snapshot manifest %s/%s: %w", bucket, key, err)
		logger.ErrorContext(ctx, "backup.s3.get_failed", slog.String("err", wrapped.Error()))
		return ybSnapshotManifest{}, wrapped
	}
	var manifest ybSnapshotManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		wrapped := fmt.Errorf("unmarshal yb snapshot manifest %s/%s: %w", bucket, key, err)
		logger.ErrorContext(ctx, "backup.s3.get_failed", slog.String("err", wrapped.Error()))
		return ybSnapshotManifest{}, wrapped
	}
	return manifest, nil
}

// newestYBSnapshotRunID returns the run id of the newest export prefix in the
// bucket, or "" when no export exists yet. Run ids are UTC timestamps, so the
// lexicographic maximum is the newest.
func newestYBSnapshotRunID(ctx context.Context, client *s3.Client, bucket string) (string, error) {
	prefixes, err := listImmediatePrefixes(ctx, client, bucket, ybSnapshotRootPrefix)
	if err != nil {
		return "", err
	}
	if len(prefixes) == 0 {
		return "", nil
	}
	sort.Strings(prefixes)
	newest := prefixes[len(prefixes)-1]
	return strings.TrimSuffix(strings.TrimPrefix(newest, ybSnapshotRootPrefix), "/"), nil
}

// objectExists reports whether bucket/key exists, treating a NotFound HeadObject
// as a clean false rather than an error.
func objectExists(ctx context.Context, client *s3.Client, bucket, key string) (bool, error) {
	_, err := client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err == nil {
		return true, nil
	}
	if isObjectNotFound(err) {
		return false, nil
	}
	wrapped := fmt.Errorf("head object %s/%s: %w", bucket, key, err)
	telemetry.L(ctx).ErrorContext(ctx, "backup.s3.head_failed", slog.String("err", wrapped.Error()))
	return false, wrapped
}

// isObjectNotFound reports whether a HeadObject error means the key is absent.
// HeadObject returns no typed body, so a NotFound smithy.APIError code or a
// NoSuchKey typed error both indicate absence.
func isObjectNotFound(err error) bool {
	var notFound *s3types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var noSuchKey *s3types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		return code == "NotFound" || code == "NoSuchKey" || code == "404"
	}
	return false
}
