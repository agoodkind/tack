package ops

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"goodkind.io/tack/internal/telemetry"
)

// artifactCategory names the backup artifact shapes the verifier recognizes,
// so the dispatcher can route each artifact to its dedicated checker.
type artifactCategory int

const (
	categoryUnknown artifactCategory = iota
	categoryFDB
	categoryYugabyteTar
	categoryPgDumpYugabyte
	categoryPgDumpTemporal
	categoryMeilisearch
)

// categoryFromName maps a backup artifact filename to its category.
func categoryFromName(name string) artifactCategory {
	base := filepath.Base(name)
	switch {
	case strings.HasPrefix(base, "fdb-snapshot") && strings.HasSuffix(base, ".tar.gz"),
		strings.HasPrefix(base, "fdbbackup") && strings.HasSuffix(base, ".tar.gz"),
		strings.HasPrefix(base, "tack_fdb-data") && strings.HasSuffix(base, ".tar.gz"),
		strings.HasPrefix(base, "tack_fdb-cluster") && strings.HasSuffix(base, ".tar.gz"):
		return categoryFDB
	case strings.HasPrefix(base, "audit-snapshot") && strings.HasSuffix(base, ".tar.gz"),
		strings.HasPrefix(base, "yugabyte") && strings.HasSuffix(base, ".tar.gz"),
		strings.HasPrefix(base, "tack_yugabyte-data") && strings.HasSuffix(base, ".tar.gz"):
		return categoryYugabyteTar
	case strings.HasPrefix(base, "tack_meili-data") && strings.HasSuffix(base, ".tar.gz"),
		strings.HasPrefix(base, "meilisearch") && strings.HasSuffix(base, ".tar.gz"),
		strings.HasPrefix(base, "meili") && strings.HasSuffix(base, ".tar.gz"):
		return categoryMeilisearch
	case strings.HasSuffix(base, ".sql") || strings.HasSuffix(base, ".sql.gz"):
		if strings.Contains(base, "temporal") {
			return categoryPgDumpTemporal
		}
		return categoryPgDumpYugabyte
	default:
		return categoryUnknown
	}
}

// RunBackupVerify is the entry point for `./server ops backup verify <path>`.
// It performs three checks: (1) the manifest exists and parses, (2) every
// recorded artifact still hashes to the recorded sha256, (3) each artifact
// passes its category-specific structural sanity check.
func RunBackupVerify(ctx context.Context, backupDir string) error {
	log := slog.Default()
	ctx, span := telemetry.StartSpan(ctx, "backup.verify")
	defer span.End()

	st, err := os.Stat(backupDir)
	if err != nil {
		log.ErrorContext(ctx, "backup.verify.stat_failed",
			slog.String("dir", backupDir),
			slog.Any("err", err),
		)
		return fmt.Errorf("stat %s: %w", backupDir, err)
	}
	if !st.IsDir() {
		notDirErr := fmt.Errorf("not a directory: %s", backupDir)
		log.ErrorContext(ctx, "backup.verify.not_a_dir",
			slog.String("dir", backupDir),
			slog.Any("err", notDirErr),
		)
		return notDirErr
	}

	manifest, err := readManifest(ctx, log, backupDir)
	if err != nil {
		log.ErrorContext(ctx, "backup.verify.manifest_read_failed",
			slog.Any("err", err),
		)
		return err
	}
	log.InfoContext(ctx, "backup.verify.started",
		slog.String("dir", backupDir),
		slog.Int("manifest_entries", len(manifest)),
	)

	hashFails, err := verifyManifestHashes(ctx, log, backupDir, manifest)
	if err != nil {
		log.ErrorContext(ctx, "backup.verify.hash_walk_failed",
			slog.Any("err", err),
		)
		return err
	}

	shapeFails := verifyArtifactShapes(ctx, log, backupDir, manifest)

	totalFails := len(hashFails) + len(shapeFails)
	if totalFails > 0 {
		for _, f := range hashFails {
			log.ErrorContext(ctx, "backup.verify.hash_fail",
				slog.String("detail", f),
				slog.Any("err", fmt.Errorf("%s", f)),
			)
		}
		for _, f := range shapeFails {
			log.ErrorContext(ctx, "backup.verify.shape_fail",
				slog.String("detail", f),
				slog.Any("err", fmt.Errorf("%s", f)),
			)
		}
		return fmt.Errorf("backup verify failed: %d hash mismatch(es), %d shape failure(s)",
			len(hashFails), len(shapeFails))
	}
	log.InfoContext(ctx, "backup.verify.ok",
		slog.Int("artifacts_checked", len(manifest)),
	)
	return nil
}

// verifyManifestHashes recomputes sha256 for every recorded artifact and
// returns any mismatches. The verification gate the plan calls out: SHA-256
// of the on-disk file must equal the manifest's recorded digest.
func verifyManifestHashes(ctx context.Context, log *slog.Logger, backupDir string, entries []manifestEntry) ([]string, error) {
	var fails []string
	for _, e := range entries {
		path := filepath.Join(backupDir, e.RelPath)
		got, size, err := hashFileSHA256(ctx, log, path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				fails = append(fails, e.RelPath+" (missing)")
				continue
			}
			return nil, err
		}
		if got != e.SHA256 {
			fails = append(fails, fmt.Sprintf("%s (sha mismatch: manifest=%s got=%s)",
				e.RelPath, e.SHA256, got))
			continue
		}
		if size != e.Size {
			fails = append(fails, fmt.Sprintf("%s (size mismatch: manifest=%d got=%d)",
				e.RelPath, e.Size, size))
		}
	}
	return fails, nil
}

// verifyArtifactShapes routes each artifact to a structural check and
// returns the list of failures.
func verifyArtifactShapes(ctx context.Context, log *slog.Logger, backupDir string, entries []manifestEntry) []string {
	var fails []string
	for _, e := range entries {
		path := filepath.Join(backupDir, e.RelPath)
		cat := categoryFromName(e.RelPath)
		var err error
		switch cat {
		case categoryFDB:
			err = checkFDBArchive(path)
		case categoryYugabyteTar:
			err = checkYugabyteTar(path)
		case categoryPgDumpYugabyte, categoryPgDumpTemporal:
			err = checkPgDump(path)
		case categoryMeilisearch:
			err = checkMeilisearch(path)
		case categoryUnknown:
			continue
		}
		if err != nil {
			fails = append(fails, fmt.Sprintf("%s: %s", e.RelPath, err))
			continue
		}
		log.DebugContext(ctx, "backup.verify.shape_ok",
			slog.String("artifact", e.RelPath),
		)
	}
	return fails
}

// checkFDBArchive walks the tar.gz inventory and asserts the archive carries
// either fdbbackup snapshot markers or raw .sqlite/.fdq files. Returns a plain
// error so the caller logs once with the aggregated detail.
func checkFDBArchive(path string) error {
	names, err := ListTarGz(path)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("archive is empty (0 entries); 2026-04-25 empty-backup signature")
	}
	hasSnapshots, hasLogs, hasKvranges, hasProps, hasSqlite := false, false, false, false, false
	for _, n := range names {
		switch {
		case strings.Contains(n, "/snapshots/snapshot,") || strings.HasPrefix(n, "snapshots/snapshot,"):
			hasSnapshots = true
		case strings.Contains(n, "/logs/") || strings.HasPrefix(n, "logs/"):
			hasLogs = true
		case strings.Contains(n, "/kvranges/") || strings.HasPrefix(n, "kvranges/"):
			hasKvranges = true
		case strings.HasSuffix(n, "/properties/log_begin_version") ||
			strings.HasSuffix(n, "properties/log_begin_version"):
			hasProps = true
		case strings.HasSuffix(n, ".sqlite") || strings.HasSuffix(n, ".fdq"):
			hasSqlite = true
		}
	}
	if hasSnapshots {
		if !hasLogs || !hasKvranges || !hasProps {
			return fmt.Errorf("fdbbackup archive missing logs/, kvranges/, or properties/log_begin_version")
		}
		return nil
	}
	if hasSqlite {
		return nil
	}
	return fmt.Errorf("no fdbbackup snapshot marker AND no .sqlite/.fdq files (2026-04-25 empty-backup signature)")
}

// checkYugabyteTar checks volume-tar shape by requiring at least one Yugabyte
// data marker (yb-data, pg_data, postgresql.conf, or a tablet- entry).
func checkYugabyteTar(path string) error {
	names, err := ListTarGz(path)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("archive is empty (0 entries)")
	}
	for _, n := range names {
		if strings.Contains(n, "yb-data") ||
			strings.Contains(n, "pg_data") ||
			strings.Contains(n, "postgresql.conf") ||
			strings.Contains(n, "tablet-") {
			return nil
		}
	}
	return fmt.Errorf("volume tar has no Yugabyte data markers")
}

// checkPgDump asserts the dump contains a CREATE TABLE in its first 16 MiB.
// Handles plain .sql and .sql.gz transparently.
func checkPgDump(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		slog.Error("backup.verify.pgdump.stat_failed", slog.String("path", path), slog.Any("err", err))
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if st.Size() == 0 {
		emptyErr := fmt.Errorf("dump file is empty (0 bytes)")
		slog.Error("backup.verify.pgdump.empty", slog.String("path", path), slog.Any("err", emptyErr))
		return emptyErr
	}
	r, err := openMaybeGzip(path)
	if err != nil {
		return err
	}
	defer r.Close()
	limited := io.LimitReader(r, 16*1024*1024)
	body, err := io.ReadAll(limited)
	if err != nil && !errors.Is(err, io.EOF) {
		slog.Error("backup.verify.pgdump.read_failed", slog.String("path", path), slog.Any("err", err))
		return fmt.Errorf("read dump %s: %w", path, err)
	}
	if !strings.Contains(string(body), "CREATE TABLE") {
		noTableErr := fmt.Errorf("no CREATE TABLE in first 16 MiB")
		slog.Error("backup.verify.pgdump.no_create_table", slog.String("path", path), slog.Any("err", noTableErr))
		return noTableErr
	}
	return nil
}

// checkMeilisearch asserts the volume tar contains a data.ms/ entry or
// instance-uid file.
func checkMeilisearch(path string) error {
	names, err := ListTarGz(path)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("archive is empty")
	}
	for _, n := range names {
		if strings.Contains(n, "data.ms/") || strings.Contains(n, "data.ms") ||
			strings.Contains(n, "instance-uid") {
			return nil
		}
	}
	return fmt.Errorf("no data.ms/ or instance-uid (suspicious empty Meilisearch volume)")
}

// ListTarGz returns the member names in a gzipped tar archive without
// extracting. Replaces the shell `tar -tzf` pipe.
func ListTarGz(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		slog.Error("backup.verify.tar.open_failed", slog.String("path", path), slog.Any("err", err))
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		slog.Error("backup.verify.tar.gunzip_failed", slog.String("path", path), slog.Any("err", err))
		return nil, fmt.Errorf("gunzip %s: %w", path, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			slog.Error("backup.verify.tar.read_failed", slog.String("path", path), slog.Any("err", err))
			return nil, fmt.Errorf("tar read %s: %w", path, err)
		}
		names = append(names, h.Name)
	}
	return names, nil
}

// openMaybeGzip returns a reader transparently handling .gz suffix.
func openMaybeGzip(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		slog.Error("backup.verify.open_failed", slog.String("path", path), slog.Any("err", err))
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			_ = f.Close()
			slog.Error("backup.verify.gunzip_failed", slog.String("path", path), slog.Any("err", err))
			return nil, fmt.Errorf("gunzip %s: %w", path, err)
		}
		return &gzipReadCloser{gz: gz, f: f}, nil
	}
	return f, nil
}

type gzipReadCloser struct {
	gz *gzip.Reader
	f  *os.File
}

func (g *gzipReadCloser) Read(p []byte) (int, error) {
	n, err := g.gz.Read(p)
	if err == nil {
		return n, nil
	}
	if errors.Is(err, io.EOF) {
		return n, io.EOF
	}
	return n, fmt.Errorf("gzip read: %w", err)
}

func (g *gzipReadCloser) Close() error {
	gzErr := g.gz.Close()
	fErr := g.f.Close()
	if gzErr != nil {
		slog.Error("backup.verify.gzip_close_failed", slog.Any("err", gzErr))
		return fmt.Errorf("gzip close: %w", gzErr)
	}
	if fErr != nil {
		slog.Error("backup.verify.file_close_failed", slog.Any("err", fErr))
		return fmt.Errorf("file close: %w", fErr)
	}
	return nil
}
