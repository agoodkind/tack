package testenv

import (
	"strconv"
	"strings"
	"testing"
)

const (
	// openSearchFillerPath is the file that fills the data path.
	openSearchFillerPath = openSearchDataPath + "/tack-disk-filler"
	// fillChunkBytes is the dd block size of the filler.
	fillChunkBytes = 1 << 20
)

// DataUsage returns the size and the used bytes of the data path, read with
// df inside the engine.
func (e *DisposableEngine) DataUsage(t *testing.T) (int64, int64) {
	t.Helper()
	output := e.run(t, "df", "-B1", "--output=size,used", openSearchDataPath)
	lines := strings.Split(strings.TrimSpace(output), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) != 2 {
		t.Fatalf("read data path usage from %q", output)
	}
	size, sizeErr := strconv.ParseInt(fields[0], 10, 64)
	used, usedErr := strconv.ParseInt(fields[1], 10, 64)
	if sizeErr != nil || usedErr != nil {
		t.Fatalf("parse data path usage %q: %v %v", output, sizeErr, usedErr)
	}
	return size, used
}

// FillData writes a filler file until the data path is at least percent
// used, and returns the size and used bytes after the write.
func (e *DisposableEngine) FillData(t *testing.T, percent int64) (int64, int64) {
	t.Helper()
	size, used := e.DataUsage(t)
	missing := size*percent/100 - used
	if missing > 0 {
		chunks := (missing + fillChunkBytes - 1) / fillChunkBytes
		e.run(t, "dd", "if=/dev/zero", "of="+openSearchFillerPath,
			"bs="+strconv.Itoa(fillChunkBytes), "count="+strconv.FormatInt(chunks, 10))
	}
	return e.DataUsage(t)
}

// FreeData removes the filler file and returns the size and used bytes
// after the removal.
func (e *DisposableEngine) FreeData(t *testing.T) (int64, int64) {
	t.Helper()
	e.run(t, "rm", "-f", openSearchFillerPath)
	return e.DataUsage(t)
}
