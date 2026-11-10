// The snapshot manifest defines completeness for the export orchestrator,
// node archive commands, and restore drill. The orchestrator lists its
// artifacts at the run root and every live tablet-server node with a node
// prefix. It uploads its manifest after its other artifacts. Each data guest
// then uploads a tablet archive and its inventory under that prefix.
//
// The restore drill requires every declared artifact and required node
// artifact to exist. Discovery skips incomplete runs; an explicit run
// selection fails when its run is incomplete.
//
// An incomplete declaration differs from a missing object. A manifest that
// omits a required restore artifact needs a new export. A declaration with
// an absent object requires re-upload or another export. Both conditions
// refuse the run and identify the failure.

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
	// ybSnapshotRootPrefix specifies the object-store prefix with one child
	// prefix per export run.
	ybSnapshotRootPrefix = "yugabyte-snapshot/"
	// ybSnapshotManifestObject is the manifest's object base name under the
	// run's key prefix.
	ybSnapshotManifestObject = "manifest.json"
	// ybSnapshotMetadataObject is the exported snapshot metadata's object base
	// name under the run's key prefix.
	ybSnapshotMetadataObject = "metadata.snapshot"
	// ybSnapshotSchemaObject is the schema dump's object base name under the
	// run's key prefix.
	ybSnapshotSchemaObject = "schema.sql"
	// ybSnapshotRolesObject is the roles dump's object base name under the
	// run's key prefix.
	ybSnapshotRolesObject = "roles.sql"
	// ybNodeArchiveObject is the tablet archive's object base name under each
	// node's prefix.
	ybNodeArchiveObject = "tablets.tar.gz"
	// ybNodeInventoryObject specifies the inventory object beside the tablet
	// archive under the same node prefix.
	ybNodeInventoryObject = "tablets.inventory"
	// ybSnapshotRunIDLayout formats an export run ID as a UTC timestamp. The
	// staleness check dates the newest complete run from that key instead of
	// using a separate marker.
	ybSnapshotRunIDLayout = "20060102T150405Z"
)

// ybSnapshotManifestNode records the tablet-server name from yb-admin and
// its run-relative archive prefix, such as nodes/yb1/.
type ybSnapshotManifestNode struct {
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
}

// ybSnapshotManifest records an export's snapshot, published artifacts at
// the run root, and node prefixes that require archives before restore.
type ybSnapshotManifest struct {
	RunID      string `json:"run_id"`
	SnapshotID string `json:"snapshot_id"`
	Database   string `json:"database"`
	CreatedAt  string `json:"created_at"`
	// Artifacts lists run-relative object names at the run root. Each declared
	// object must exist before restore. New declarations automatically join the
	// presence check. The list must also include every ybRequiredRunArtifacts
	// entry that the restore opens regardless of the declaration.
	Artifacts []string                 `json:"artifacts"`
	Nodes     []ybSnapshotManifestNode `json:"nodes"`
}

// ybRunIDPattern matches the run ID shape from
// opsNow().UTC().Format(ybSnapshotRunIDLayout). Staging uses the ID in
// [filepath.Join] and recursively removes the resulting directory. The pattern
// must reject path characters.
var ybRunIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z$`)

// ybNodeNamePattern accepts DNS-style node names and unbracketed IPv6
// literals from hostFromHostPort. These values form staging filenames and
// container extraction paths. The pattern rejects separators, whitespace,
// and shell metacharacters.
var ybNodeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]*$`)

// ybArtifactNamePattern accepts base names for artifacts at the run root.
// Those values form object keys and staging filenames. Like node names,
// they must not include separators, whitespace, or shell metacharacters.
var ybArtifactNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validate rejects unsafe strings before they form filesystem paths. An
// object-store manifest can contain corrupt or attacker-supplied values.
// Validation checks RunID, artifact names, node names, and derived node
// prefixes after decoding and before upload.
func (m ybSnapshotManifest) validate() error {
	if !ybRunIDPattern.MatchString(m.RunID) {
		return fmt.Errorf("manifest run_id %q is not a run-key timestamp", m.RunID)
	}
	for _, artifact := range m.Artifacts {
		if err := validateYBArtifactName(artifact); err != nil {
			return err
		}
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

// validateFetchedUnder validates the decoded manifest and requires its
// RunID to match the object-store prefix used to fetch it. Node archive
// checks, restore staging, and export dating use the manifest's RunID. A
// manifest under another run's prefix could select the wrong object keys,
// artifacts, and timestamp without this check.
func (m ybSnapshotManifest) validateFetchedUnder(runID string) error {
	if err := m.validate(); err != nil {
		return err
	}
	if m.RunID != runID {
		return fmt.Errorf("manifest run_id %q is not the run %q it was fetched under", m.RunID, runID)
	}
	return nil
}

// validateYBNodeName checks the node-name allowlist and separately rejects
// traversal components. The pattern accepts repeated dots. The .. check
// adds a restriction. Explicit slash checks also preserve that restriction
// if the pattern changes later.
func validateYBNodeName(name string) error {
	if !ybNodeNamePattern.MatchString(name) {
		return fmt.Errorf("manifest node name %q is not a host name or IPv6 literal", name)
	}
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, `\`) {
		return fmt.Errorf("manifest node name %q contains a path traversal component", name)
	}
	return nil
}

// validateYBArtifactName checks the artifact-name allowlist and traversal
// components before a name can select a staging file or object key.
func validateYBArtifactName(name string) error {
	if !ybArtifactNamePattern.MatchString(name) {
		return fmt.Errorf("manifest artifact name %q is not an object base name", name)
	}
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, `\`) {
		return fmt.Errorf("manifest artifact name %q contains a path traversal component", name)
	}
	return nil
}

// newYBSnapshotManifest records root artifact names in upload order and a
// node entry for each tablet-server name. It sorts node names for stable
// node ordering within the manifest.
func newYBSnapshotManifest(
	runID, snapshotID, database string,
	nodeNames, artifactNames []string,
) ybSnapshotManifest {
	sorted := make([]string, len(nodeNames))
	copy(sorted, nodeNames)
	sort.Strings(sorted)
	nodes := make([]ybSnapshotManifestNode, 0, len(sorted))
	for _, name := range sorted {
		nodes = append(nodes, ybSnapshotManifestNode{Name: name, Prefix: "nodes/" + name + "/"})
	}
	artifacts := make([]string, len(artifactNames))
	copy(artifacts, artifactNames)
	return ybSnapshotManifest{
		RunID:      runID,
		SnapshotID: snapshotID,
		Database:   database,
		CreatedAt:  opsNow().UTC().Format(time.RFC3339),
		Artifacts:  artifacts,
		Nodes:      nodes,
	}
}

// nodePrefix returns a node's run-relative prefix and reports false when
// the manifest does not list the node.
func (m ybSnapshotManifest) nodePrefix(name string) (string, bool) {
	for _, node := range m.Nodes {
		if node.Name == name {
			return node.Prefix, true
		}
	}
	return "", false
}

// ybRunArtifactKey returns a root artifact's full object key for a run.
func ybRunArtifactKey(runID, artifact string) string {
	return ybSnapshotKeyPrefix(runID) + artifact
}

// ybRequiredRunArtifacts returns the artifacts that restore opens by name.
// ybDrillArtifactPath selects each artifact. The completeness check rejects
// a declaration that omits a required artifact before probing object presence.
//
//   - Exported snapshot metadata supplies tablet mappings.
//   - The schema recreates tables and their grants.
//   - Roles supply the identities those grants require.
func ybRequiredRunArtifacts() []string {
	return []string{ybSnapshotMetadataObject, ybSnapshotSchemaObject, ybSnapshotRolesObject}
}

// undeclaredYBRequiredArtifacts returns required artifacts that the manifest
// omits, in required-list order. It examines the declaration without reading
// the object store. missingYBRunArtifacts checks object presence separately.
func undeclaredYBRequiredArtifacts(manifest ybSnapshotManifest) []string {
	var undeclared []string
	for _, required := range ybRequiredRunArtifacts() {
		if !slices.Contains(manifest.Artifacts, required) {
			undeclared = append(undeclared, required)
		}
	}
	return undeclared
}

// missingYBRunArtifacts returns declared root artifact names without an
// object in the store. A finished run has uploaded these before its
// manifest. A missing object indicates an interrupted upload or later loss;
// the restore would lack the definitions or data in that artifact.
func missingYBRunArtifacts(manifest ybSnapshotManifest, exists func(key string) (bool, error)) ([]string, error) {
	var missing []string
	for _, artifact := range manifest.Artifacts {
		present, err := exists(ybRunArtifactKey(manifest.RunID, artifact))
		if err != nil {
			return nil, err
		}
		if !present {
			missing = append(missing, artifact)
		}
	}
	return missing, nil
}

// ybTabletServerAliveStatus specifies the Status value for a live server
// in yb-admin list_all_tablet_servers output.
const ybTabletServerAliveStatus = "ALIVE"

// parseYBTabletServers reads node names from yb-admin
// list_all_tablet_servers output. Each data row starts with an undashed UUID
// that matches ybObjectIDPattern, followed by an RPC host:port in column two
// and Status in column four. It includes only ALIVE servers; a DEAD server
// cannot publish its tablet archive and would prevent run completeness
// while it remains unavailable.
//
// The Compose deployment advertises permanent node names instead of
// addresses. The parser removes ports, deduplicates names, and sorts them.
func parseYBTabletServers(stdout string) []string {
	seen := map[string]bool{}
	for line := range strings.SplitSeq(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || !ybObjectIDPattern.MatchString(fields[0]) {
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

// hostFromHostPort removes a port and IPv6 brackets from a host:port pair.
// If [net.SplitHostPort] fails, it trims outer brackets from the input.
func hostFromHostPort(hostPort string) string {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		return strings.Trim(hostPort, "[]")
	}
	return host
}

// fetchYBSnapshotManifest downloads and decodes one run's manifest into
// memory. It rejects invalid run IDs or node entries and a RunID that
// differs from the fetch prefix before callers construct paths, keys, or
// commands from them.
//
// A missing manifest returns a wrapped object-not-found error without an
// error log. The orchestrator uploads the manifest last, and discovery
// walks skip prefixes without one.
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
	if err := manifest.validateFetchedUnder(runID); err != nil {
		wrapped := fmt.Errorf("yb snapshot manifest %s/%s: %w", bucket, key, err)
		logger.ErrorContext(ctx, "backup.s3.get_failed", slog.String("err", wrapped.Error()))
		return ybSnapshotManifest{}, wrapped
	}
	return manifest, nil
}

// listYBSnapshotRunIDs returns bucket run-prefix suffixes in ascending
// order. Generated IDs use UTC timestamps, making the last generated ID
// chronologically newest.
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

// newestUploadedYBSnapshotManifest searches run prefixes from newest to
// oldest and returns the first existing manifest. It skips prefixes without
// a manifest and logs one summary; those runs may be unfinished or
// abandoned because the orchestrator uploads the manifest last. It returns
// found=false if every prefix lacks a manifest.
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

// logSkippedYBRuns logs one summary for skipped prefixes without manifests
// instead of a separate message for each run.
func logSkippedYBRuns(ctx context.Context, skipped []string) {
	if len(skipped) == 0 {
		return
	}
	telemetry.L(ctx).InfoContext(ctx, "backup.yb_snapshot.runs_skipped",
		slog.Any("run_ids", skipped), slog.String("reason", "manifest not uploaded"))
}

// objectExists checks bucket/key and returns false without an error when
// HeadObject reports a missing object.
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

// isObjectNotFound checks whether an object-store error reports an absent
// key. It accepts typed NotFound and NoSuchKey errors and smithy.APIError
// codes NotFound, NoSuchKey, or 404. HeadObject does not provide a typed
// response body for missing keys.
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
