package testenv_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerRejectsArguments(t *testing.T) {
	cases := []struct {
		name      string
		arguments []string
	}{
		{name: "missing selection", arguments: []string{"-count=1", "-timeout=1m"}},
		{name: "empty selection", arguments: []string{"-run=", "-count=1", "-timeout=1m"}},
		{name: "duplicate selection", arguments: []string{"-run=A", "-run=B", "-count=1", "-timeout=1m"}},
		{name: "zero count", arguments: []string{"-run=A", "-count=0", "-timeout=1m"}},
		{name: "missing count", arguments: []string{"-run=A", "-timeout=1m"}},
		{name: "zero timeout", arguments: []string{"-run=A", "-count=1", "-timeout=0"}},
		{name: "invalid timeout", arguments: []string{"-run=A", "-count=1", "-timeout=minute"}},
		{name: "unknown flag", arguments: []string{"-run=A", "-count=1", "-timeout=1m", "-json"}},
		{name: "package", arguments: []string{"-run=A", "-count=1", "-timeout=1m", "./..."}},
		{name: "command", arguments: []string{"test", "-run=A", "-count=1", "-timeout=1m"}},
		{name: "false verbose", arguments: []string{"-run=A", "-count=1", "-timeout=1m", "-v=false"}},
		{name: "duplicate verbose", arguments: []string{"-run=A", "-count=1", "-timeout=1m", "-v", "-v=true"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			options := testenv.MacTestOptions{Root: t.TempDir(), Image: macImage, EvidenceDirectory: filepath.Join(t.TempDir(), "evidence"), TestArguments: test.arguments}
			if result, err := testenv.RunMacTests(t.Context(), options); err == nil || result.ExitCode != 1 {
				t.Fatalf("RunMacTests accepted invalid arguments: result=%+v error=%v", result, err)
			}
			if _, err := os.Lstat(options.EvidenceDirectory); !os.IsNotExist(err) {
				t.Fatalf("invalid arguments created evidence: %v", err)
			}
		})
	}
}

func TestMacRunnerAcceptsVerboseForms(t *testing.T) {
	for _, flags := range [][]string{{"-v"}, {"-v=true"}, {"-v", "true"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			options, _ := macFixture(t)
			options.TestArguments = append(slices.Clone(options.TestArguments), flags...)
			macPrepare(t, options)
		})
	}
}

func TestMacRunnerRejectsEvidenceReuse(t *testing.T) {
	for _, existing := range []string{"directory", "broken symlink", "checkout parent"} {
		t.Run(existing, func(t *testing.T) {
			options, _ := macFixture(t)
			switch existing {
			case "directory":
				if err := os.Mkdir(options.EvidenceDirectory, 0o700); err != nil {
					t.Fatal(err)
				}
			case "broken symlink":
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), options.EvidenceDirectory); err != nil {
					t.Fatal(err)
				}
			case "checkout parent":
				options.EvidenceDirectory = filepath.Dir(options.Root)
			}
			if _, err := testenv.RunMacTests(t.Context(), options); err == nil || strings.Contains(err.Error(), "execution is not implemented") {
				t.Fatalf("RunMacTests accepted existing evidence: %v", err)
			}
		})
	}
}

func TestMacRunnerRejectsCheckoutAndImage(t *testing.T) {
	for _, invalid := range []string{"module", "nested root", "integration", "image", "canceled"} {
		t.Run(invalid, func(t *testing.T) {
			options, _ := macFixture(t)
			ctx := t.Context()
			switch invalid {
			case "module":
				macWrite(t, options.Root, "go.mod", "module example.com/other\n")
			case "nested root":
				options.Root = filepath.Join(options.Root, "internal")
			case "integration":
				if err := os.Remove(filepath.Join(options.Root, "internal/test/integration/example_test.go")); err != nil {
					t.Fatal(err)
				}
			case "image":
				options.Image = "latest"
			case "canceled":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			if _, err := testenv.RunMacTests(ctx, options); err == nil || strings.Contains(err.Error(), "execution is not implemented") {
				t.Fatalf("RunMacTests accepted invalid %s: %v", invalid, err)
			}
		})
	}
}

func TestMacRunnerResolvesSymlinks(t *testing.T) {
	options, _ := macFixture(t)
	root := options.Root
	alias := filepath.Join(t.TempDir(), "checkout")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	parentAlias := filepath.Join(t.TempDir(), "evidence-parent")
	if err := os.Symlink(parent, parentAlias); err != nil {
		t.Fatal(err)
	}
	options.Root = alias
	options.EvidenceDirectory = filepath.Join(parentAlias, "evidence")
	invocation := macPrepare(t, options)
	if invocation.Root != root || invocation.Service.WorkingDir != root {
		t.Fatalf("runner did not resolve checkout root: %+v", invocation)
	}
	if _, err := os.Stat(filepath.Join(parent, "evidence/source.sha256")); err != nil {
		t.Fatalf("runner did not resolve evidence parent: %v", err)
	}
}
