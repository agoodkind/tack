//go:build darwin && integration

package testenv_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"

	"goodkind.io/tack/internal/testenv"
)

const macImage = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type macInvocation struct {
	Root     string                         `json:"root"`
	Project  string                         `json:"project"`
	Service  types.ServiceConfig            `json:"service"`
	Networks map[string]types.NetworkConfig `json:"networks"`
}

func macFixture(t *testing.T) (testenv.MacTestOptions, *git.Repository) {
	t.Helper()
	t.Setenv("COMPOSE_PROJECT_NAME", "")
	t.Setenv("TACK_SEARCH_CLUSTER", "1")
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
	compose, err := os.ReadFile("testdata/mac-runner-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	macWrite(t, root, "go.mod", "module goodkind.io/tack\n")
	macWrite(t, root, "internal/test/integration/example_test.go", "package integration\n")
	macWrite(t, root, "docker-compose.test.yml", string(compose))
	macWrite(t, root, ".gitignore", "ignored.go\n.env\n")
	worktree, err := repository.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("."); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Commit("Add test checkout", &git.CommitOptions{Author: &object.Signature{
		Name: "Test", Email: "test@example.com", When: time.Now(),
	}}); err != nil {
		t.Fatal(err)
	}
	return testenv.MacTestOptions{
		Root: root, Image: macImage, EvidenceDirectory: filepath.Join(t.TempDir(), "evidence"),
		TestArguments: []string{"-run", "^TestSearch($|[^A-M])", "-count=3", "-timeout", "1h25m", "-tags", "runtimeaudit,integration", "-skip=^Skip$"},
	}, repository
}

func macWrite(t *testing.T, root, path, contents string) {
	t.Helper()
	absolute := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absolute, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func macPrepare(t *testing.T, options testenv.MacTestOptions) macInvocation {
	t.Helper()
	result, err := testenv.RunMacTests(t.Context(), options)
	if err == nil || !strings.Contains(err.Error(), "inspect required immutable local image") || result.ExitCode != 1 || result.RunnerID != "" {
		t.Fatalf("preparation must refuse the absent immutable image: result=%+v error=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(result.EvidenceDirectory, "runner-request.json")); !os.IsNotExist(err) {
		t.Fatalf("source preparation attempted container creation: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(result.EvidenceDirectory, "invocation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var invocation macInvocation
	if err := json.Unmarshal(contents, &invocation); err != nil {
		t.Fatal(err)
	}
	return invocation
}

func TestMacRunnerPreparation(t *testing.T) {
	options, repository := macFixture(t)
	arguments := slices.Clone(options.TestArguments)
	invocation := macPrepare(t, options)
	if !slices.Equal(arguments, options.TestArguments) {
		t.Fatal("RunMacTests changed the caller's arguments")
	}
	expected := []string{"test", "-tags=fdb,runtimeaudit,integration", "-run", "^TestSearch($|[^A-M])", "-count=3", "-timeout=1h25m", "-v", "-skip", "^Skip$", "./internal/test/integration/..."}
	if !slices.Equal(invocation.Service.Command, expected) || !slices.Equal(invocation.Service.Entrypoint, []string{"/usr/local/go/bin/go"}) {
		t.Fatalf("unexpected Go command: %+v", invocation.Service)
	}
	head, err := repository.Head()
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Service.Image != macImage || invocation.Service.WorkingDir != options.Root || invocation.Service.Build != nil {
		t.Fatalf("unexpected runner identity: %+v", invocation.Service)
	}
	if *invocation.Service.Environment["TACK_TEST_SOURCE_REVISION"] != head.Hash().String() || *invocation.Service.Environment["TACK_SEARCH_CLUSTER"] != "1" {
		t.Fatalf("unexpected test environment: %+v", invocation.Service.Environment)
	}
	for _, volume := range invocation.Service.Volumes {
		if volume.ReadOnly {
			t.Fatalf("runner volume is read-only: %+v", volume)
		}
		if volume.Target == "/var/run/docker.sock" && volume.Source != "/var/run/docker.sock.raw" {
			t.Fatalf("runner did not use the raw socket: %+v", volume)
		}
	}
	information, err := os.Stat(options.EvidenceDirectory)
	if err != nil || information.Mode().Perm() != 0o700 {
		t.Fatalf("evidence must be private: information=%v error=%v", information, err)
	}
}
