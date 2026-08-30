// backup_yb_manifest.go is the completeness contract between the yb snapshot
// export orchestrator, the per-node archive command, and the restore drill.
// The orchestrator writes a manifest listing every live tablet-server node and
// the object-store prefix that node must fill, and uploads it last so a
// manifest's presence implies every other run artifact landed; each data
// guest's archive command uploads its own tablet tar under its prefix; the
// restore drill never uses a run whose manifest lists a node prefix with no
// archive object behind it (skipping it in discovery, refusing it when the
// run was requested explicitly).

package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"regexp"
	"slices"
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
	// ybSnapshotMetadataObject is the exported snapshot metadata's object base
	// name under the run's key prefix.
	ybSnapshotMetadataObject = "metadata.snapshot"
	// ybNodeArchiveObject is the tablet archive's object base name under each
	// node's prefix.
	ybNodeArchiveObject = "tablets.tar.gz"
	// ybSnapshotRunIDLayout is the layout the export orchestrator formats a run
	// id with. A run id is therefore a UTC timestamp, which is what lets the
	// staleness check date the newest complete run without a marker.
	ybSnapshotRunIDLayout = "20060102T150405Z"
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

// ybRunIDPattern matches the run ids the export orchestrator generates,
// opsNow().UTC().Format(ybSnapshotRunIDLayout). RunID feeds [filepath.Join] on
// staging dirs that are recursively removed, so nothing looser may pass.
var ybRunIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z$`)

// ybNodeNamePattern matches the node identities hostFromHostPort produces for
// the manifest: DNS names or unbracketed IPv6 literals. Node names feed staged
// file names and in-container extraction paths, so no separator, whitespace,
// or shell metacharacter may pass.
var ybNodeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]*$`)

// validate rejects a manifest whose strings could escape the paths built from
// them. The manifest is fetched from the object store, so a corrupted or
// attacker-written manifest is untrusted input. Validation runs at both trust
// boundaries, decode after fetch and write before upload, so every downstream
// use of RunID, node names, and node prefixes handles only vetted strings.
func (m ybSnapshotManifest) validate() error {
	if !ybRunIDPattern.MatchString(m.RunID) {
		return fmt.Errorf("manifest run_id %q is not a run-key timestamp", m.RunID)
	}
	for _, node := range m.Nodes {
		if err := validateYBNodeName(node.Name); err != nil {
			return err
		}
		derived := "nodes/" + node.Name + "/"
		if node.Prefix != derived {
			return fmt.Errorf("manifest node %q prefix %q is not the derived %q",
				node.Name, node.Prefix, derived)
		}
	}
	return nil
}

// validateYBNodeName enforces the node-name allowlist. The explicit
// traversal-substring checks are redundant with the pattern today; they keep a
// future pattern relaxation from silently reopening path traversal.
func validateYBNodeName(name string) error {
	if !ybNodeNamePattern.MatchString(name) {
		return fmt.Errorf("manifest node name %q is not a host name or IPv6 literal", name)
	}
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, `\`) {
		return fmt.Errorf("manifest node name %q contains a path traversal component", name)
	}
	return nil
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

// ybTabletServerAliveStatus is the Status column value of a live tablet
// server in yb-admin list_all_tablet_servers output.
const ybTabletServerAliveStatus = "ALIVE"

// ybTabletServerUUIDPattern matches the server id column of yb-admin
// list_all_tablet_servers, which prints 32 hex characters with no dashes
// (observed live against 2024.2.8.0), unlike the dashed snapshot ids
// ybUUIDPattern matches.
var ybTabletServerUUIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// parseYBTabletServers extracts the tablet-server node names from yb-admin
// list_all_tablet_servers output. Data rows start with the undashed server
// UUID, carry the advertised RPC host:port in the second column, and report
// liveness in the fourth (Status) column. Only ALIVE servers are included: a DEAD server
// can never archive its tablets, so listing it in the manifest would block
// the completeness gate and every restore forever. The host is the node name
// the compose deploy announces (never an address, per the identity contract
// in docker-compose.yml). Names are deduplicated and sorted.
func parseYBTabletServers(stdout string) []string {
	seen := map[string]bool{}
	for line := range strings.SplitSeq(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || !ybTabletServerUUIDPattern.MatchString(fields[0]) {
			continue
		}
		if fields[3] != ybTabletServerAliveStatus {
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

// fetchYBSnapshotManifest downloads, decodes, and validates the manifest of
// one export run; a manifest whose run id or node entries fail validation is
// refused here so no downstream path builds paths or commands from them. The
// manifest is small, so it is read into memory rather than staged. A missing
// manifest comes back as a NotFound-wrapped error without an error log,
// because the orchestrator uploads the manifest last and walkers over the run
// prefixes probe for it, treating absence as a skip rather than a failure.
func fetchYBSnapshotManifest(ctx context.Context, client *s3.Client, bucket, runID string) (ybSnapshotManifest, error) {
	logger := telemetry.L(ctx)
	key := ybSnapshotKeyPrefix(runID) + ybSnapshotManifestObject
	out, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		wrapped := fmt.Errorf("get yb snapshot manifest %s/%s: %w", bucket, key, err)
		if !isObjectNotFound(err) {
			logger.ErrorContext(ctx, "backup.s3.get_failed", slog.String("err", wrapped.Error()))
		}
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
	if err := manifest.validate(); err != nil {
		wrapped := fmt.Errorf("yb snapshot manifest %s/%s: %w", bucket, key, err)
		logger.ErrorContext(ctx, "backup.s3.get_failed", slog.String("err", wrapped.Error()))
		return ybSnapshotManifest{}, wrapped
	}
	return manifest, nil
}

// listYBSnapshotRunIDs returns every export run id in the bucket, sorted
// ascending. Run ids are UTC timestamps, so the last element is the newest.
func listYBSnapshotRunIDs(ctx context.Context, client *s3.Client, bucket string) ([]string, error) {
	prefixes, err := listImmediatePrefixes(ctx, client, bucket, ybSnapshotRootPrefix)
	if err != nil {
		return nil, err
	}
	runIDs := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		runIDs = append(runIDs, strings.TrimSuffix(strings.TrimPrefix(prefix, ybSnapshotRootPrefix), "/"))
	}
	sort.Strings(runIDs)
	return runIDs, nil
}

// newestUploadedYBSnapshotManifest walks the export run prefixes newest-first
// and returns the newest run whose manifest object exists. The orchestrator
// uploads the manifest last, so a manifest-less prefix is a run that has not
// finished (or never will); those are skipped and logged in one summary rather
// than treated as errors. found is false when no run has a manifest yet.
func newestUploadedYBSnapshotManifest(
	ctx context.Context,
	client *s3.Client,
	bucket string,
) (manifest ybSnapshotManifest, found bool, err error) {
	runIDs, err := listYBSnapshotRunIDs(ctx, client, bucket)
	if err != nil {
		return manifest, false, err
	}
	var skipped []string
	for _, runID := range slices.Backward(runIDs) {
		candidate, fetchErr := fetchYBSnapshotManifest(ctx, client, bucket, runID)
		if fetchErr != nil {
			if isObjectNotFound(fetchErr) {
				skipped = append(skipped, runID)
				continue
			}
			return manifest, false, fetchErr
		}
		logSkippedYBRuns(ctx, skipped)
		return candidate, true, nil
	}
	logSkippedYBRuns(ctx, skipped)
	return manifest, false, nil
}

// logSkippedYBRuns logs one summary for the manifest-less run prefixes a walk
// skipped, so the skips stay visible without a log line per run.
func logSkippedYBRuns(ctx context.Context, skipped []string) {
	if len(skipped) == 0 {
		return
	}
	telemetry.L(ctx).InfoContext(ctx, "backup.yb_snapshot.runs_skipped",
		slog.Any("run_ids", skipped), slog.String("reason", "manifest not uploaded"))
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
