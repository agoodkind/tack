//go:build darwin && integration && macrunnerfixture

package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/testenv"
)

func macCommandFixture(t *testing.T) (testenv.MacTestOptions, *client.Client) {
	t.Helper()
	image := os.Getenv("TACK_TESTENV_RUNNER_IMAGE")
	if image == "" {
		t.Fatal("set TACK_TESTENV_RUNNER_IMAGE to the required existing immutable image ID")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	configuration, err := repository.Config()
	if err != nil {
		t.Fatal(err)
	}
	configuration.Raw.Section("commit").SetOption("gpgSign", "false")
	if err := repository.SetConfig(configuration); err != nil {
		t.Fatal(err)
	}
	for source, target := range map[string]string{
		"../../internal/testenv/testdata/mac-runner-compose.yml":          "docker-compose.test.yml",
		"../../internal/testenv/testdata/macrunnerfixture/runner_test.go": "internal/test/integration/example_test.go",
	} {
		contents, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, target)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module goodkind.io/tack\n\ngo 1.27\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("."); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Commit("Add command fixture", &git.CommitOptions{Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(root))
	project := fmt.Sprintf("tack-command-fixture-%x", digest[:8])
	t.Setenv("COMPOSE_PROJECT_NAME", project)
	docker, err := client.New(client.WithHost("unix://" + filepath.Join(os.Getenv("HOME"), ".docker/run/docker.sock")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	for _, suffix := range []string{"tack-test-go-cache", "tack-test-go-mod"} {
		name := project + "_" + suffix
		if _, err := docker.VolumeCreate(t.Context(), client.VolumeCreateOptions{Name: name, Labels: map[string]string{"tack.mac-runner-fixture": project}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			current, err := docker.VolumeInspect(ctx, name, client.VolumeInspectOptions{})
			if err != nil || current.Volume.Labels["tack.mac-runner-fixture"] != project {
				t.Errorf("refuse fixture volume removal %s: %v", name, err)
				return
			}
			if _, err := docker.VolumeRemove(ctx, name, client.VolumeRemoveOptions{}); err != nil {
				t.Error(err)
			}
		})
	}
	directory := filepath.Join(t.TempDir(), "evidence")
	if parent := os.Getenv("TACK_TESTENV_RUNNER_EVIDENCE_PARENT"); parent != "" {
		directory = filepath.Join(parent, project)
	}
	return testenv.MacTestOptions{Root: root, Image: image, EvidenceDirectory: directory}, docker
}
