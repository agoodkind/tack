//go:build linux

// backup_s3_object_stream_test.go downloads an object much larger than the
// memory a restore drill may hold, through the same fetch the drill stages
// snapshot artifacts with, from a real S3-compatible store. It needs the
// cachestat system call (Linux 6.5 and later) to read how much of the staged
// file sits in the page cache, which the kernel charges to the drill
// container's memory limit, and a disk filesystem under TMPDIR, since
// cachestat sees no cache through overlayfs.

package ops

import (
	"bytes"
	"crypto/sha256"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

const (
	// streamTestObjectBytes is the downloaded object's size: 32 times the
	// cache bound, so a fetch that lets the whole object pile up
	// in the page cache cannot pass.
	streamTestObjectBytes = 32 * stagingSyncBytes
	// streamTestCacheLimit is the most staged data the test lets sit in the
	// page cache, and the most it lets sit dirty or under writeback, at once:
	// one sync interval, plus one more for a sample taken after a chunk's
	// writes and before its sync and release.
	streamTestCacheLimit = 2 * stagingSyncBytes
	// streamTestHeapGrowthLimit is the most the Go heap may grow during the
	// download. The copy needs one small buffer; this leaves room for the
	// HTTP client and the sampler and is still a small fraction of the
	// object.
	streamTestHeapGrowthLimit = 16 << 20
	// streamTestSampleInterval spaces the sampler's readings.
	streamTestSampleInterval = 2 * time.Millisecond
)

// streamPeaks is what the sampler observed while the download ran.
type streamPeaks struct {
	unwrittenBytes uint64
	cachedBytes    uint64
	heapInuseBytes uint64
}

// TestGetObjectToFileBoundsUnwrittenStagingData downloads an object 32 times
// the sync interval through getObjectToFile and requires the staged file to
// match the object byte for byte, the staged data held in the page cache, and
// the part of it dirty or under writeback, never to exceed two sync intervals,
// and the Go heap to grow by less than streamTestHeapGrowthLimit.
func TestGetObjectToFileBoundsUnwrittenStagingData(t *testing.T) {
	endpoint := testenv.ObjectStore(t)
	ctx := t.Context()
	cfg := &config.Config{
		BackupS3Endpoint:   endpoint,
		BackupS3AccessKey:  "test-access", // gitleaks:allow test placeholder
		BackupS3SecretKey:  "test-secret", // gitleaks:allow test placeholder
		BackupS3Region:     "us-east-1",
		BackupS3BucketMain: "tack-stream-test",
	}
	client := newBackupS3Client(cfg)
	if err := ensureBucket(ctx, client, cfg.BackupS3BucketMain); err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	wantSum := writeStreamTestSource(t, source)
	key := "yugabyte-snapshot/stream-test/nodes/yb1/tablets.tar.gz"
	if err := putObjectFromFile(ctx, client, cfg.BackupS3BucketMain, key, source); err != nil {
		t.Fatalf("upload the test object: %v", err)
	}

	staged := filepath.Join(dir, "staged")
	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	stop := make(chan struct{})
	var peaks streamPeaks
	var sampler sync.WaitGroup
	sampler.Go(func() { peaks = sampleStaging(t, staged, stop) })
	fetchErr := getObjectToFile(ctx, client, cfg.BackupS3BucketMain, key, staged)
	close(stop)
	sampler.Wait()
	if fetchErr != nil {
		t.Fatalf("getObjectToFile: %v", fetchErr)
	}

	if gotSum := fileSum(t, staged); !bytes.Equal(gotSum, wantSum) {
		t.Fatalf("staged file sha256 %x, want %x", gotSum, wantSum)
	}
	t.Logf("object %d bytes; peak unwritten staged data %d bytes of %d cached; heap in use %d bytes before, %d peak",
		streamTestObjectBytes, peaks.unwrittenBytes, peaks.cachedBytes, before.HeapInuse, peaks.heapInuseBytes)
	if peaks.cachedBytes == 0 {
		t.Fatal("cachestat saw none of the staged file cached, so it cannot measure this filesystem " +
			"(overlayfs reports nothing); run with TMPDIR on a disk filesystem such as a Docker volume")
	}
	if peaks.unwrittenBytes > streamTestCacheLimit {
		t.Errorf("staged data sitting dirty or under writeback peaked at %d bytes, want at most %d",
			peaks.unwrittenBytes, streamTestCacheLimit)
	}
	if peaks.cachedBytes > streamTestCacheLimit {
		t.Errorf("staged data held in the page cache peaked at %d bytes, want at most %d",
			peaks.cachedBytes, streamTestCacheLimit)
	}
	if peaks.heapInuseBytes > before.HeapInuse+streamTestHeapGrowthLimit {
		t.Errorf("heap in use peaked at %d bytes from %d, want growth under %d",
			peaks.heapInuseBytes, before.HeapInuse, streamTestHeapGrowthLimit)
	}
}

// sampleStaging reads the staged file's cached and unwritten bytes and the
// heap in use until stop closes, and returns the largest of each. A read that
// finds no file yet is skipped; a cachestat failure fails the test, because
// without it the bound is not measured.
func sampleStaging(t *testing.T, path string, stop <-chan struct{}) streamPeaks {
	var peaks streamPeaks
	pageSize := uint64(os.Getpagesize())
	ticker := time.NewTicker(streamTestSampleInterval)
	defer ticker.Stop()
	for {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		peaks.heapInuseBytes = max(peaks.heapInuseBytes, stats.HeapInuse)
		if file, err := os.Open(path); err == nil {
			var cache unix.Cachestat_t
			statErr := unix.Cachestat(uint(file.Fd()), &unix.CachestatRange{Off: 0, Len: 0}, &cache, 0)
			_ = file.Close()
			if statErr != nil {
				t.Errorf("cachestat %s: %v (the test needs Linux 6.5 or later)", path, statErr)
				return peaks
			}
			peaks.unwrittenBytes = max(peaks.unwrittenBytes, (cache.Dirty+cache.Writeback)*pageSize)
			peaks.cachedBytes = max(peaks.cachedBytes, cache.Cache*pageSize)
		}
		select {
		case <-stop:
			return peaks
		case <-ticker.C:
		}
	}
}

// writeStreamTestSource writes streamTestObjectBytes of seeded pseudo-random
// data to path, which no compressing or deduplicating layer can shrink, and
// returns its sha256.
func writeStreamTestSource(t *testing.T, path string) []byte {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()
	generator := rand.NewChaCha8([32]byte{'t', 'a', 'c', 'k'})
	hash := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(file, hash), generator, streamTestObjectBytes); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return hash.Sum(nil)
}

// fileSum returns the sha256 of the file at path.
func fileSum(t *testing.T, path string) []byte {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return hash.Sum(nil)
}
