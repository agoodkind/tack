// Package metadata writes build metadata for the current Git checkout.
package metadata

import (
	"fmt"
	"io"
	"os"

	"github.com/go-git/go-git/v6"

	"goodkind.io/tack/internal/ci/image/environment"
	"goodkind.io/tack/internal/clock"
)

const (
	buildTimeLayout            = "2006-01-02T15:04:05Z"
	minimumShortCommitLength   = 7
	metadataOutputFilePermBits = 0o666
)

// Write writes the selected tag or branch to stdout, then appends build
// metadata to GITHUB_OUTPUT. It returns an error without writing
// GITHUB_OUTPUT if the stdout write fails.
func Write(stdout io.Writer) error {
	outputPath, err := environment.Required("GITHUB_OUTPUT")
	if err != nil {
		return &os.PathError{Op: "read build metadata output file", Path: "GITHUB_OUTPUT", Err: err}
	}

	workingDirectory, err := os.Getwd()
	if err != nil {
		return &os.PathError{Op: "read working directory", Path: ".", Err: err}
	}

	repository, err := git.PlainOpen(workingDirectory)
	if err != nil {
		return &os.PathError{Op: "open Git checkout", Path: workingDirectory, Err: err}
	}

	defer func() { _ = repository.Close() }()

	head, err := repository.Head()
	if err != nil {
		return &os.PathError{Op: "read Git HEAD", Path: workingDirectory, Err: err}
	}

	_, err = repository.CommitObject(head.Hash())
	if err != nil {
		return &os.PathError{
			Op:   "read Git HEAD commit " + head.Hash().String(),
			Path: workingDirectory,
			Err:  err,
		}
	}

	commit := head.Hash().String()

	shortCommit, err := abbreviateCommit(repository, workingDirectory, commit)
	if err != nil {
		return err
	}

	buildTime := clock.Now().UTC().Format(buildTimeLayout)

	tag, found, err := findExactTag(repository, workingDirectory, head.Hash())
	if err != nil {
		return err
	}

	if found {
		err = printMetadataMessage(stdout, "Build metadata uses tag %s\n", tag)
	} else {
		tag, err = environment.Required("BRANCH_NAME")
		if err != nil {
			return &os.PathError{Op: "read build metadata branch", Path: "BRANCH_NAME", Err: err}
		}

		err = printMetadataMessage(
			stdout,
			"HEAD does not have an exact tag. Build metadata uses branch %s\n",
			tag,
		)
	}

	if err != nil {
		return err
	}

	content := "commit=" + commit + "\nshort=" + shortCommit + "\nbuild_time=" + buildTime +
		"\ntag=" + tag + "\n"

	return appendFile(outputPath, content)
}

func printMetadataMessage(stdout io.Writer, format, tag string) error {
	if _, err := fmt.Fprintf(stdout, format, tag); err != nil {
		return &os.PathError{Op: "write build metadata message", Path: "stdout", Err: err}
	}
	return nil
}

func appendFile(path, content string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, metadataOutputFilePermBits)
	if err != nil {
		return &os.PathError{Op: "open metadata output file", Path: path, Err: err}
	}
	if _, err := file.WriteString(content); err != nil {
		writeErr := &os.PathError{Op: "write metadata output file", Path: path, Err: err}
		_ = file.Close()
		return writeErr
	}
	if err := file.Close(); err != nil {
		return &os.PathError{Op: "close metadata output file", Path: path, Err: err}
	}
	return nil
}
