// Package ops: deploy_build_tar.go owns the small tarball builder used to
// hand the docker daemon a build context. Splitting it out keeps
// deploy_build.go focused on the build orchestration.

package ops

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// writeTarFromPaths writes a tar archive containing each named path. Paths
// are interpreted relative to the current working directory, which is the
// repo root when invoked from the operator workflow. Symlinks are recorded
// as TypeSymlink; directories are skipped because git ls-files returns
// individual files; missing entries fail the whole stream.
func writeTarFromPaths(ctx context.Context, w io.Writer, paths []string) error {
	tw := tar.NewWriter(w)
	defer func() { _ = tw.Close() }()
	for _, p := range paths {
		err := ctx.Err()
		if err != nil {
			slog.ErrorContext(ctx, "ops.deploy.build.tar_ctx_canceled",
				slog.String("err", err.Error()))
			return fmt.Errorf("ctx err: %w", err)
		}
		err = writeTarOne(ctx, tw, p)
		if err != nil {
			slog.ErrorContext(ctx, "ops.deploy.build.tar_one_failed",
				slog.String("path", p), slog.String("err", err.Error()))
			return fmt.Errorf("tar %s: %w", p, err)
		}
	}
	return nil
}

// writeTarOne packs a single path into the tar writer. Errors are logged
// before being returned so the operator's view of the build failure
// includes the underlying io cause.
func writeTarOne(ctx context.Context, tw *tar.Writer, relPath string) error {
	info, err := os.Lstat(relPath)
	if err != nil {
		slog.ErrorContext(ctx, "ops.deploy.build.tar_lstat_failed",
			slog.String("path", relPath), slog.String("err", err.Error()))
		return fmt.Errorf("lstat %s: %w", relPath, err)
	}
	hdr, err := writeTarHeader(ctx, relPath, info)
	if err != nil {
		return err
	}
	err = tw.WriteHeader(hdr)
	if err != nil {
		slog.ErrorContext(ctx, "ops.deploy.build.tar_header_write_failed",
			slog.String("path", relPath), slog.String("err", err.Error()))
		return fmt.Errorf("write header %s: %w", relPath, err)
	}
	if hdr.Typeflag != tar.TypeReg {
		return nil
	}
	f, err := os.Open(relPath)
	if err != nil {
		slog.ErrorContext(ctx, "ops.deploy.build.tar_open_failed",
			slog.String("path", relPath), slog.String("err", err.Error()))
		return fmt.Errorf("open %s: %w", relPath, err)
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(tw, f)
	if err != nil {
		slog.ErrorContext(ctx, "ops.deploy.build.tar_copy_failed",
			slog.String("path", relPath), slog.String("err", err.Error()))
		return fmt.Errorf("copy %s: %w", relPath, err)
	}
	return nil
}

// writeTarHeader builds the tar header from FileInfo, normalizing the path
// to forward slashes and recording symlink targets.
func writeTarHeader(ctx context.Context, relPath string, info os.FileInfo) (*tar.Header, error) {
	link := ""
	mode := info.Mode()
	if mode&os.ModeSymlink != 0 {
		target, err := os.Readlink(relPath)
		if err != nil {
			slog.ErrorContext(ctx, "ops.deploy.build.tar_readlink_failed",
				slog.String("path", relPath), slog.String("err", err.Error()))
			return nil, fmt.Errorf("readlink %s: %w", relPath, err)
		}
		link = target
	}
	hdr, err := tar.FileInfoHeader(info, link)
	if err != nil {
		slog.ErrorContext(ctx, "ops.deploy.build.tar_header_failed",
			slog.String("path", relPath), slog.String("err", err.Error()))
		return nil, fmt.Errorf("tar header %s: %w", relPath, err)
	}
	hdr.Name = filepath.ToSlash(relPath)
	return hdr, nil
}
