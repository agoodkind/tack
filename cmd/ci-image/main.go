// Package main formats image digests and tags for the build workflow.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type operation string

const (
	digestOperation operation = "digest"
	tagsOperation   operation = "tags"
)

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type tagInputs struct {
	registry, owner, image, shortCommit, commit, refType string
}

func required(key string) (string, error) {
	value, present := os.LookupEnv(key)
	if !present {
		return "", fmt.Errorf("%s: required environment variable is unset", key)
	}
	return value, nil
}

func writeDigest() error {
	architecture, err := required("IMAGE_ARCH")
	if err != nil {
		return err
	}
	if architecture != "amd64" && architecture != "arm64" {
		return fmt.Errorf("%s: %s", "Unsupported image architecture", architecture)
	}
	value, err := required("IMAGE_DIGEST")
	if err != nil {
		return err
	}
	if !digestPattern.MatchString(value) {
		return fmt.Errorf("%s", "Build output is not a SHA256 image digest")
	}
	directory, err := required("DIGEST_DIRECTORY")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(directory, 0o777); err != nil {
		slog.Error("digest.directory_failed", slog.String("path", directory), slog.Any("error", err))
		return fmt.Errorf("create digest directory %s: %w", directory, err)
	}
	if err := writeFile(filepath.Join(directory, architecture+".digest"), value+"\n"); err != nil {
		return err
	}
	return nil
}

func writeTags() error {
	var input tagInputs
	for _, item := range []struct {
		key   string
		value *string
	}{
		{"REGISTRY", &input.registry},
		{"IMAGE_OWNER", &input.owner},
		{"IMAGE_NAME", &input.image},
		{"COMMIT_SHORT", &input.shortCommit},
		{"GITHUB_SHA", &input.commit},
		{"GITHUB_REF_TYPE", &input.refType},
	} {
		value, err := required(item.key)
		if err != nil {
			return err
		}
		*item.value = value
	}
	base := input.registry + "/" + input.owner + "/" + input.image
	references := []string{base + ":" + input.shortCommit, base + ":" + input.commit}
	if input.refType == "tag" {
		name, err := required("GITHUB_REF_NAME")
		if err != nil {
			return err
		}
		references = append(references, base+":"+strings.ReplaceAll(name, "/", "-"), base+":latest")
	} else {
		name, err := required("BRANCH_NAME")
		if err != nil {
			return err
		}
		references = append(references, base+":"+strings.ReplaceAll(name, "/", "-"))
		if name == "main" {
			references = append(references, base+":latest")
		}
	}
	path, err := required("IMAGE_TAG_FILE")
	if err != nil {
		return err
	}
	if err := writeFile(path, strings.Join(references, "\n")+"\n"); err != nil {
		return err
	}
	return nil
}

func writeFile(path, content string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		slog.Error("image_file.open_failed", slog.String("path", path), slog.Any("error", err))
		return fmt.Errorf("open image file %s: %w", path, err)
	}
	if _, err := file.WriteString(content); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			slog.Error("image_file.close_failed", slog.String("path", path), slog.Any("error", closeErr))
		}
		slog.Error("image_file.write_failed", slog.String("path", path), slog.Any("error", err))
		return fmt.Errorf("write image file %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		slog.Error("image_file.close_failed", slog.String("path", path), slog.Any("error", err))
		return fmt.Errorf("close image file %s: %w", path, err)
	}
	return nil
}

func main() {
	var err error
	if len(os.Args) != 2 {
		err = fmt.Errorf("usage: ci-image digest|tags")
	} else {
		switch operation(os.Args[1]) {
		case digestOperation:
			err = writeDigest()
		case tagsOperation:
			err = writeTags()
		default:
			err = fmt.Errorf("unknown operation: %s", os.Args[1])
		}
	}
	if err != nil {
		slog.Error("ci-image.failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
}
