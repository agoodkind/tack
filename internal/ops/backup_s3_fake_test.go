package ops

import (
	"encoding/json"
	"encoding/xml"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"goodkind.io/tack/internal/config"
)

// fakeBackupObjectStore is an in-memory stand-in for the SeaweedFS S3 endpoint
// the backup family reads, so the object-store paths are exercised through the
// real S3 client, the real signing, and real HTTP rather than through swapped
// closures. It serves the three operations those paths issue: a delimited
// ListObjectsV2 for the run prefixes, GetObject for manifests and markers, and
// HeadObject for the node archives. Addressing is path-style, the only style
// SeaweedFS supports and the style newBackupS3Client forces.
type fakeBackupObjectStore struct {
	bucket  string
	objects map[string][]byte
}

// fakeS3ListResult is the ListObjectsV2 body S3 returns for a delimited list:
// the child folders as CommonPrefixes and the keys directly under the prefix as
// Contents.
type fakeS3ListResult struct {
	XMLName        xml.Name         `xml:"ListBucketResult"`
	Name           string           `xml:"Name"`
	Prefix         string           `xml:"Prefix"`
	Delimiter      string           `xml:"Delimiter"`
	KeyCount       int              `xml:"KeyCount"`
	MaxKeys        int              `xml:"MaxKeys"`
	IsTruncated    bool             `xml:"IsTruncated"`
	Contents       []fakeS3Object   `xml:"Contents"`
	CommonPrefixes []fakeS3Prefixes `xml:"CommonPrefixes"`
}

type fakeS3Object struct {
	Key  string `xml:"Key"`
	Size int64  `xml:"Size"`
}

type fakeS3Prefixes struct {
	Prefix string `xml:"Prefix"`
}

// newFakeBackupObjectStore starts the fake store over objects (key to body) and
// returns a client and a config pointed at it, both torn down with the test.
func newFakeBackupObjectStore(t *testing.T, bucket string, objects map[string][]byte) (*s3.Client, *config.Config) {
	t.Helper()
	store := &fakeBackupObjectStore{bucket: bucket, objects: objects}
	server := httptest.NewServer(store)
	t.Cleanup(server.Close)
	cfg := &config.Config{
		BackupS3Endpoint:   server.URL,
		BackupS3AccessKey:  "test-access", // gitleaks:allow test placeholder
		BackupS3SecretKey:  "test-secret", // gitleaks:allow test placeholder
		BackupS3Region:     "us-east-1",
		BackupS3BucketMain: bucket,
	}
	return newBackupS3Client(cfg), cfg
}

// fakeYBExportRunObjects is the object set a finished export run leaves under
// one run prefix: the manifest the walk reads, every run-root artifact the
// manifest declares, and, per node the manifest lists, every artifact that
// node's archive run publishes, which is what the completeness gate probes for.
// The manifest is placed under prefixRunID whatever run it declares, so a
// manifest that names a run other than its own prefix can be exercised.
func fakeYBExportRunObjects(t *testing.T, prefixRunID string, manifest ybSnapshotManifest) map[string][]byte {
	t.Helper()
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal yb snapshot manifest: %v", err)
	}
	prefix := ybSnapshotKeyPrefix(prefixRunID)
	objects := map[string][]byte{prefix + ybSnapshotManifestObject: body}
	for _, artifact := range manifest.Artifacts {
		objects[prefix+artifact] = []byte("export artifact " + artifact)
	}
	for _, node := range manifest.Nodes {
		for _, object := range ybNodeArtifactObjects() {
			objects[prefix+node.Prefix+object] = fakeYBNodeArtifact(manifest, node, object)
		}
	}
	return objects
}

// fakeYBNodeArtifact is the body of one node artifact in the fake store. The
// inventory is rendered through the production writer for the manifest's own
// run and node, recording no files, so the drill's staging step reads it back
// the way it reads a real one; every other node artifact is opaque bytes.
func fakeYBNodeArtifact(manifest ybSnapshotManifest, node ybSnapshotManifestNode, object string) []byte {
	if object == ybNodeInventoryObject {
		return ybArchiveInventory{RunID: manifest.RunID, Node: node.Name, Files: nil}.render()
	}
	return []byte("node artifact")
}

func (s *fakeBackupObjectStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if bucket != s.bucket {
		writeFakeS3Error(w, r, http.StatusNotFound, "NoSuchBucket")
		return
	}
	if r.URL.Query().Get("list-type") == "2" {
		s.writeList(w, r.URL.Query().Get("prefix"), r.URL.Query().Get("delimiter"))
		return
	}
	body, found := s.objects[key]
	if !found {
		writeFakeS3Error(w, r, http.StatusNotFound, "NoSuchKey")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(body)
}

// writeList answers a delimited ListObjectsV2: a key with the delimiter still
// in its remainder collapses into its first folder, which is what makes the
// export run prefixes discoverable. Keys are listed in ascending order, as S3
// lists them, which is the order the FoundationDB backup selection reads as
// oldest first.
func (s *fakeBackupObjectStore) writeList(w http.ResponseWriter, prefix, delimiter string) {
	result := fakeS3ListResult{
		Name:        s.bucket,
		Prefix:      prefix,
		Delimiter:   delimiter,
		MaxKeys:     1000,
		IsTruncated: false,
	}
	seenPrefix := map[string]bool{}
	keys := slices.Sorted(maps.Keys(s.objects))
	for _, key := range keys {
		body := s.objects[key]
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		remainder := strings.TrimPrefix(key, prefix)
		folder, _, nested := strings.Cut(remainder, delimiter)
		if delimiter != "" && nested {
			common := prefix + folder + delimiter
			if !seenPrefix[common] {
				seenPrefix[common] = true
				result.CommonPrefixes = append(result.CommonPrefixes, fakeS3Prefixes{Prefix: common})
			}
			continue
		}
		result.Contents = append(result.Contents, fakeS3Object{Key: key, Size: int64(len(body))})
	}
	result.KeyCount = len(result.Contents) + len(result.CommonPrefixes)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_ = xml.NewEncoder(w).Encode(result)
}

// writeFakeS3Error answers with the error code the SDK maps to a typed error.
// A HEAD carries no body, which is exactly how S3 reports a missing key to
// HeadObject.
func writeFakeS3Error(w http.ResponseWriter, r *http.Request, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write([]byte("<Error><Code>" + code + "</Code><Message>" + code + "</Message></Error>"))
}
