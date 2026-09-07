package ops

import (
	"encoding/xml"
	"io"
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
// closures. It serves the four operations those paths issue: a delimited
// ListObjectsV2 for the run prefixes, GetObject for manifests and markers,
// HeadObject for the node archives, and PutObject for the markers and the
// shared alarm memory. Addressing is path-style, the only style
// SeaweedFS supports and the style newBackupS3Client forces.
type fakeBackupObjectStore struct {
	bucket  string
	objects map[string][]byte
	// refusePut, while set, answers every PutObject with AccessDenied, the
	// shape of a store that still serves reads but refuses writes (TACK-481).
	refusePut bool
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
	_, client, cfg := startFakeBackupObjectStore(t, bucket, objects)
	return client, cfg
}

// startFakeBackupObjectStore is newFakeBackupObjectStore returning the store
// as well, for a test that changes how it answers between runs.
func startFakeBackupObjectStore(
	t *testing.T,
	bucket string,
	objects map[string][]byte,
) (*fakeBackupObjectStore, *s3.Client, *config.Config) {
	t.Helper()
	store := &fakeBackupObjectStore{bucket: bucket, objects: objects, refusePut: false}
	server := httptest.NewServer(store)
	t.Cleanup(server.Close)
	cfg := &config.Config{
		BackupS3Endpoint:   server.URL,
		BackupS3AccessKey:  "test-access", // gitleaks:allow test placeholder
		BackupS3SecretKey:  "test-secret", // gitleaks:allow test placeholder
		BackupS3Region:     "us-east-1",
		BackupS3BucketMain: bucket,
	}
	return store, newBackupS3Client(cfg), cfg
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
	if r.Method == http.MethodPut {
		s.storePut(w, r, key)
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

// storePut answers a PutObject by keeping the body under key, so what one run
// writes is what the next run reads back. A small seekable body carries its
// checksum in a header, not a trailing chunk, so the bytes are the object's own.
func (s *fakeBackupObjectStore) storePut(w http.ResponseWriter, r *http.Request, key string) {
	if s.refusePut {
		writeFakeS3Error(w, r, http.StatusForbidden, "AccessDenied")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeFakeS3Error(w, r, http.StatusBadRequest, "IncompleteBody")
		return
	}
	s.objects[key] = body
	w.WriteHeader(http.StatusOK)
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
