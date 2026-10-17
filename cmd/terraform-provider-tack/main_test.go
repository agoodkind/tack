package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

var foundationDBPackages = []string{
	"github.com/apple/foundationdb/",
	"goodkind.io/tack/internal/adapters/foundationdb",
}

// TestProviderDependsOnNeitherFoundationDBNorCgo loads the provider and its
// dependencies with golang.org/x/tools/go/packages using the server build
// settings: cgo enabled and the fdb build tag. It fails if a dependency is a
// FoundationDB package or a package outside the standard library uses cgo. It
// detects cgo through compiled Go files that cgo processing writes to the
// build cache outside the package directory.
func TestProviderDependsOnNeitherFoundationDBNorCgo(t *testing.T) {
	loaded, err := packages.Load(&packages.Config{
		Context:    t.Context(),
		Mode:       packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedDeps | packages.NeedModule,
		Env:        append(os.Environ(), "CGO_ENABLED=1"),
		BuildFlags: []string{"-tags=fdb"},
	}, ".")
	if err != nil {
		t.Fatalf("load the provider packages: %v", err)
	}
	visited := 0
	packages.Visit(loaded, nil, func(loadedPackage *packages.Package) {
		visited++
		for _, loadError := range loadedPackage.Errors {
			t.Errorf("load %s: %v", loadedPackage.PkgPath, loadError)
		}
		for _, prefix := range foundationDBPackages {
			if strings.HasPrefix(loadedPackage.PkgPath, prefix) {
				t.Errorf("the provider depends on the FoundationDB package %s", loadedPackage.PkgPath)
			}
		}
		if loadedPackage.Module == nil {
			return
		}
		for _, compiled := range loadedPackage.CompiledGoFiles {
			if filepath.Dir(compiled) != loadedPackage.Dir {
				t.Errorf("the provider depends on %s, which has cgo files", loadedPackage.PkgPath)
				return
			}
		}
	})
	if visited == 0 {
		t.Fatal("loading the provider returned no packages")
	}
}
