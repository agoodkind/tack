package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// foundationDBPackages are import path fragments of the FoundationDB bindings
// and of the Tack adapter that uses them.
var foundationDBPackages = []string{
	"github.com/apple/foundationdb/",
	"goodkind.io/tack/internal/adapters/foundationdb",
}

// TestProviderDependsOnNeitherFoundationDBNorCgo lists the dependency graph of
// the provider with cgo and the fdb build tag on, the settings of the server
// build. No package may be a FoundationDB package, and no package outside the
// standard library may contain cgo files. The provider must also build with
// CGO_ENABLED=0.
func TestProviderDependsOnNeitherFoundationDBNorCgo(t *testing.T) {
	list := exec.CommandContext(t.Context(), "go", "list", "-deps", "-tags=fdb",
		"-f", "{{.ImportPath}} {{.Standard}} {{len .CgoFiles}}", ".")
	list.Env = append(os.Environ(), "CGO_ENABLED=1")
	output, err := list.CombinedOutput()
	if err != nil {
		t.Fatalf("go list the provider dependencies: %v\n%s", err, output)
	}
	for line := range strings.Lines(string(output)) {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("go list printed %q, want an import path, a standard flag, and a cgo file count", line)
		}
		importPath, standard, cgoFiles := fields[0], fields[1], fields[2]
		for _, fragment := range foundationDBPackages {
			if strings.Contains(importPath, fragment) {
				t.Errorf("the provider depends on the FoundationDB package %s", importPath)
			}
		}
		if standard == "false" && cgoFiles != "0" {
			t.Errorf("the provider depends on %s, which has %s cgo files", importPath, cgoFiles)
		}
	}

	build := exec.CommandContext(t.Context(), "go", "build", "-buildvcs=false",
		"-o", filepath.Join(t.TempDir(), "terraform-provider-tack"), ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the provider with CGO_ENABLED=0: %v\n%s", err, output)
	}
}
