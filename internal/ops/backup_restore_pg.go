package ops

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const scratchPgImage = "postgres:16-alpine"

// pgRole names the dump-source role for sanity-query dispatch.
type pgRole string

const (
	pgRoleYugabyte pgRole = "yugabyte"
	pgRoleTemporal pgRole = "temporal-db"
)

// restorePgDump replays a pg_dump artifact into a fresh scratch Postgres
// container and runs a per-role sanity query. Mirrors restore_pg_dump in
// scripts/backup-restore-test.sh:377-452.
func restorePgDump(ctx context.Context, r *restoreCtx, artifactPath string, role pgRole) error {
	// generate a 16-byte random hex string for the scratch container password
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Errorf("generate scratch password: %w", err)
	}
	scratchPassword := hex.EncodeToString(buf)

	label := filepath.Base(artifactPath)
	r.Log.InfoContext(ctx, "backup.restore_pg.start",
		slog.String("artifact", label),
		slog.String("role", string(role)),
	)

	containerName := "tack-rt-pg-" + string(role) + "-" + r.RunID
	err := startScratchPostgres(ctx, r, containerName, scratchPassword)
	if err != nil {
		return err
	}
	r.trackContainer(containerName)

	err = waitForExec(ctx, r.Cli, containerName, 30*time.Second,
		[]string{"pg_isready", "-U", "postgres"})
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_pg.never_ready",
			slog.String("container", containerName),
			slog.Any("err", err),
		)
		return fmt.Errorf("scratch postgres never ready: %w", err)
	}

	err = streamDumpToPsql(ctx, r, containerName, artifactPath)
	if err != nil {
		return err
	}

	switch role {
	case pgRoleYugabyte:
		return assertYugabyteAuditPresent(ctx, r, containerName)
	case pgRoleTemporal:
		return assertTemporalSchemaPresent(ctx, r, containerName)
	}
	return nil
}

// startScratchPostgres launches a fresh postgres container using the caller-supplied
// password, which must be generated fresh for each invocation via crypto/rand.
func startScratchPostgres(ctx context.Context, r *restoreCtx, name, password string) error {
	cfg := &container.Config{
		Image: scratchPgImage,
		Env: []string{
			"POSTGRES_PASSWORD=" + password,
			"POSTGRES_DB=rttest",
		},
	}
	created, err := r.Cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     cfg,
		HostConfig: &container.HostConfig{},
		Name:       name,
	})
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_pg.create_failed",
			slog.String("name", name),
			slog.Any("err", err),
		)
		return fmt.Errorf("create scratch pg: %w", err)
	}
	_, err = r.Cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{})
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_pg.start_failed",
			slog.String("name", name),
			slog.Any("err", err),
		)
		return fmt.Errorf("start scratch pg: %w", err)
	}
	return nil
}

// streamDumpToPsql pipes the dump file's contents through psql inside the
// scratch container. Handles plain .sql and .sql.gz transparently.
//
// We avoid os/exec for the streaming side because the docker SDK's
// ContainerExecAttach gives us a typed stdin pipe. The shell script's
// SIGPIPE-aware reader pipeline becomes a single [io.Copy] here; Go's
// runtime handles broken-pipe semantics natively without a SIGPIPE
// handler.
func streamDumpToPsql(ctx context.Context, r *restoreCtx, name, dumpPath string) error {
	src, err := openMaybeGzip(dumpPath)
	if err != nil {
		return err
	}
	defer src.Close()

	created, err := r.Cli.ExecCreate(ctx, name, client.ExecCreateOptions{
		Cmd:          []string{"psql", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "rttest"},
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_pg.exec_create_failed",
			slog.Any("err", err),
		)
		return fmt.Errorf("exec create psql: %w", err)
	}
	att, err := r.Cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_pg.exec_attach_failed",
			slog.Any("err", err),
		)
		return fmt.Errorf("exec attach psql: %w", err)
	}
	defer att.Close()

	copyErr := make(chan error, 1)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				panicErr := fmt.Errorf("stream panic: %v", rec)
				slog.ErrorContext(ctx, "backup.restore_pg.stream_panic",
					slog.Any("err", panicErr),
					slog.Any("panic", rec),
				)
				copyErr <- panicErr
			}
		}()
		_, copyDone := io.Copy(att.Conn, src)
		_ = att.CloseWrite()
		copyErr <- copyDone
	}()
	// Drain the demultiplexed stream so the exec finishes on its own.
	buf := make([]byte, 4096)
	for {
		_, rerr := att.Reader.Read(buf)
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				r.Log.WarnContext(ctx, "backup.restore_pg.stream_drain_err",
					slog.Any("err", rerr),
				)
			}
			break
		}
	}
	err = <-copyErr
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_pg.stream_failed",
			slog.Any("err", err),
		)
		return fmt.Errorf("stream dump: %w", err)
	}
	insp, err := r.Cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_pg.exec_inspect_failed",
			slog.Any("err", err),
		)
		return fmt.Errorf("exec inspect: %w", err)
	}
	if insp.ExitCode != 0 {
		psqlErr := fmt.Errorf("psql exited %d (syntax error or schema conflict)", insp.ExitCode)
		r.Log.ErrorContext(ctx, "backup.restore_pg.psql_exit_nonzero",
			slog.Int("exit", insp.ExitCode),
			slog.Any("err", psqlErr),
		)
		return psqlErr
	}
	return nil
}

// assertYugabyteAuditPresent runs `SELECT count(*) FROM audit.events` and
// requires a positive count.
func assertYugabyteAuditPresent(ctx context.Context, r *restoreCtx, name string) error {
	res, err := containerExec(ctx, r.Cli, name,
		[]string{
			"psql", "-U", "postgres", "-d", "rttest", "-tAc",
			"SELECT count(*) FROM audit.events",
		}, nil)
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_pg.audit_query_exec_failed",
			slog.Any("err", err),
		)
		return fmt.Errorf("audit.events query: %w", err)
	}
	if res.ExitCode != 0 {
		auditQueryErr := fmt.Errorf("audit.events query exited %d", res.ExitCode)
		r.Log.ErrorContext(ctx, "backup.restore_pg.audit_query_failed",
			slog.Int("exit", res.ExitCode),
			slog.String("stderr", res.Stderr),
			slog.Any("err", auditQueryErr),
		)
		return auditQueryErr
	}
	count, err := strconv.Atoi(strings.TrimSpace(res.Stdout))
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_pg.audit_count_parse_failed",
			slog.String("stdout", res.Stdout),
			slog.Any("err", err),
		)
		return fmt.Errorf("audit.events count parse %q: %w", res.Stdout, err)
	}
	if count == 0 {
		zeroRowsErr := fmt.Errorf("audit.events restored with 0 rows")
		r.Log.ErrorContext(ctx, "backup.restore_pg.audit_zero_rows",
			slog.Any("err", zeroRowsErr),
		)
		return zeroRowsErr
	}
	r.Log.InfoContext(ctx, "backup.restore_pg.yugabyte_ok",
		slog.Int("audit_events", count),
	)
	return nil
}

// assertTemporalSchemaPresent verifies the Temporal schema came across.
func assertTemporalSchemaPresent(ctx context.Context, r *restoreCtx, name string) error {
	res, err := containerExec(ctx, r.Cli, name,
		[]string{
			"psql", "-U", "postgres", "-d", "rttest", "-tAc",
			"SELECT count(*) FROM information_schema.tables WHERE table_schema='public'",
		}, nil)
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_pg.temporal_count_exec_failed",
			slog.Any("err", err),
		)
		return fmt.Errorf("temporal table count query: %w", err)
	}
	if res.ExitCode != 0 {
		countErr := fmt.Errorf("temporal count exited %d", res.ExitCode)
		r.Log.ErrorContext(ctx, "backup.restore_pg.temporal_count_failed",
			slog.Int("exit", res.ExitCode),
			slog.String("stderr", res.Stderr),
			slog.Any("err", countErr),
		)
		return countErr
	}
	count, err := strconv.Atoi(strings.TrimSpace(res.Stdout))
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_pg.temporal_count_parse_failed",
			slog.String("stdout", res.Stdout),
			slog.Any("err", err),
		)
		return fmt.Errorf("table count parse %q: %w", res.Stdout, err)
	}
	if count < 5 {
		fewTablesErr := fmt.Errorf("expected at least 5 Temporal tables, got %d", count)
		r.Log.ErrorContext(ctx, "backup.restore_pg.temporal_too_few_tables",
			slog.Int("count", count),
			slog.Any("err", fewTablesErr),
		)
		return fewTablesErr
	}
	res, err = containerExec(ctx, r.Cli, name,
		[]string{
			"psql", "-U", "postgres", "-d", "rttest", "-tAc",
			"SELECT count(*) FROM information_schema.tables WHERE table_name='executions'",
		}, nil)
	if err != nil {
		r.Log.ErrorContext(ctx, "backup.restore_pg.executions_probe_exec_failed",
			slog.Any("err", err),
		)
		return fmt.Errorf("executions probe: %w", err)
	}
	if res.ExitCode != 0 {
		probeErr := fmt.Errorf("executions probe exited %d", res.ExitCode)
		r.Log.ErrorContext(ctx, "backup.restore_pg.executions_probe_failed",
			slog.Int("exit", res.ExitCode),
			slog.Any("err", probeErr),
		)
		return probeErr
	}
	if strings.TrimSpace(res.Stdout) == "0" {
		noExecErr := fmt.Errorf("temporal 'executions' table missing from restored schema")
		r.Log.ErrorContext(ctx, "backup.restore_pg.temporal_no_executions",
			slog.Any("err", noExecErr),
		)
		return noExecErr
	}
	return nil
}

// ExtractTarGz untars a .tar.gz into destDir. Defends against tar slip by
// rejecting paths that escape destDir.
func ExtractTarGz(tarPath, destDir string) error {
	f, err := os.Open(tarPath)
	if err != nil {
		slog.Error("backup.tar.open_failed", slog.String("path", tarPath), slog.Any("err", err))
		return fmt.Errorf("open %s: %w", tarPath, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		slog.Error("backup.tar.gunzip_failed", slog.String("path", tarPath), slog.Any("err", err))
		return fmt.Errorf("gunzip %s: %w", tarPath, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	cleanDest := filepath.Clean(destDir)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			slog.Error("backup.tar.read_failed", slog.String("path", tarPath), slog.Any("err", err))
			return fmt.Errorf("tar read: %w", err)
		}
		// Defense in depth against tar slip; reject paths that escape destDir.
		// Clean the entry name before joining so that path components like
		// ".." are resolved before the prefix check.
		cleanName := filepath.Clean(h.Name)
		clean := filepath.Join(cleanDest, cleanName)
		if clean != cleanDest && !strings.HasPrefix(clean, cleanDest+string(os.PathSeparator)) {
			slipErr := fmt.Errorf("tar entry escapes destination: %s", h.Name)
			slog.Error("backup.tar.slip_detected",
				slog.String("entry", h.Name),
				slog.String("dest", destDir),
				slog.Any("err", slipErr),
			)
			return slipErr
		}
		switch h.Typeflag {
		case tar.TypeDir:
			err := os.MkdirAll(clean, 0o750)
			if err != nil {
				slog.Error("backup.tar.mkdir_failed", slog.String("path", clean), slog.Any("err", err))
				return fmt.Errorf("mkdir %s: %w", clean, err)
			}
		case tar.TypeReg:
			err := os.MkdirAll(filepath.Dir(clean), 0o750)
			if err != nil {
				slog.Error("backup.tar.mkdir_parent_failed",
					slog.String("path", filepath.Dir(clean)),
					slog.Any("err", err),
				)
				return fmt.Errorf("mkdir %s: %w", filepath.Dir(clean), err)
			}
			err = writeTarRegular(clean, tr, h.Mode)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// writeTarRegular writes one regular tar member to dest, capping mode at
// 0700 so the extracted tree never grants world or group access.
func writeTarRegular(dest string, tr io.Reader, mode int64) error {
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(mode&0o700))
	if err != nil {
		slog.Error("backup.tar.create_failed", slog.String("dest", dest), slog.Any("err", err))
		return fmt.Errorf("create %s: %w", dest, err)
	}
	defer out.Close()
	_, err = io.Copy(out, tr)
	if err != nil {
		slog.Error("backup.tar.write_failed", slog.String("dest", dest), slog.Any("err", err))
		return fmt.Errorf("write %s: %w", dest, err)
	}
	return nil
}
