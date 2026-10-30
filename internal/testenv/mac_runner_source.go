package testenv

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
)

func macRunnerRoot(root string) (string, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", macRunnerFailure("resolve checkout root "+root, err)
	}
	repository, err := openMacRunnerRepository(resolved)
	if err != nil {
		return "", err
	}
	if err := repository.Close(); err != nil {
		return "", macRunnerFailure("close Git checkout "+resolved, err)
	}
	module, err := os.ReadFile(filepath.Join(resolved, "go.mod"))
	if err != nil {
		return "", macRunnerFailure("read checkout module "+resolved, err)
	}
	if modfile.ModulePath(module) != "goodkind.io/tack" {
		return "", fmt.Errorf("checkout %s must use module goodkind.io/tack", resolved)
	}
	integration := filepath.Join(resolved, "internal/test/integration")
	entries, err := os.ReadDir(integration)
	if err != nil {
		return "", macRunnerFailure("read integration package "+integration, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), "_test.go") {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("integration package %s must contain Go tests", integration)
}

func createMacRunnerEvidence(root, directory string) (string, error) {
	parent, err := filepath.EvalSymlinks(filepath.Dir(directory))
	if err != nil {
		return "", macRunnerFailure("resolve evidence parent "+filepath.Dir(directory), err)
	}
	directory = filepath.Join(parent, filepath.Base(directory))
	if macRunnerWithin(directory, root) {
		return "", errors.New("evidence directory must not contain the checkout")
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		return "", macRunnerFailure("create new evidence directory "+directory, err)
	}
	return directory, nil
}

func macRunnerWithin(parent, path string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func macRunnerSelected(path string) bool {
	name := filepath.Base(path)
	if name == "Makefile" || strings.HasPrefix(name, "Dockerfile") {
		return true
	}
	return slices.Contains([]string{".go", ".sql", ".json", ".yml", ".yaml", ".py"}, filepath.Ext(path))
}

func macRunnerFileHash(root, path string) ([32]byte, error) {
	contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return [32]byte{}, macRunnerFailure("read source file "+path, err)
	}
	return sha256.Sum256(contents), nil
}

// verifySources rehashes the historical selected-file manifest.
func (prepared *macRunnerPreparation) verifySources() error {
	for _, source := range prepared.source.manifest {
		digest, err := macRunnerFileHash(prepared.root, source.path)
		if err != nil {
			return err
		}
		if digest != source.digest {
			return fmt.Errorf("source hash changed: %s", source.path)
		}
	}
	return nil
}

func (prepared *macRunnerPreparation) writeSourceEvidence() error {
	var hashes strings.Builder
	for _, source := range prepared.source.manifest {
		_, _ = fmt.Fprintf(&hashes, "%x  %s\n", source.digest, source.path)
	}
	files := map[string]string{
		"revision.stdout":     prepared.source.revision + "\n",
		"dirty.stdout":        prepared.source.dirty,
		"source-paths.stdout": strings.Join(prepared.source.paths, "\n") + "\n",
		"source.sha256":       hashes.String(),
	}
	for name, contents := range files {
		path := filepath.Join(prepared.options.EvidenceDirectory, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			return macRunnerFailure("write source evidence "+path, err)
		}
	}
	invocation, err := json.MarshalIndent(macRunnerInvocation{
		SchemaVersion: 1, Name: prepared.name,
		Root: prepared.root, Project: prepared.project.Name, Service: prepared.service,
		Networks: prepared.project.Networks,
	}, "", "  ")
	if err != nil {
		return macRunnerFailure("encode runner preparation", err)
	}
	path := filepath.Join(prepared.options.EvidenceDirectory, "invocation.json")
	if err := os.WriteFile(path, append(invocation, '\n'), 0o600); err != nil {
		return macRunnerFailure("write runner preparation "+path, err)
	}
	slog.Info("testenv.run_mac.evidence_written", slog.String("root", prepared.root), slog.String("evidence", prepared.options.EvidenceDirectory))
	return nil
}

func macRunnerFailure(operation string, err error) error {
	slog.Error("testenv.run_mac.failed", slog.String("operation", operation), slog.String("err", err.Error()))
	return fmt.Errorf("%s: %w", operation, err)
}
