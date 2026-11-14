package testselection_test

import (
	"bytes"
	"go/build"
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/tack/internal/ci/testselection"
)

func TestRunSelectsIntegrationFilesPerPackage(t *testing.T) {
	root := moduleFixture(t)
	writeFixture(t, root, "zeta/ordinary_test.go", testSource("", "TestOrdinary"))
	writeFixture(t, root, "zeta/tagged_test.go", testSource("integration", "TestZeta"))
	writeFixture(
		t,
		root,
		"alpha/tagged_test.go",
		testSource(
			"integration",
			"TestBeta",
			"TestAlpha",
			"TestEngineChild",
			"TestMain",
			"Testhelper",
		),
	)
	writeFixture(t, root, "alpha/child/tagged_test.go", testSource("integration", "TestChild"))
	writeFixture(t, root, "alpha/empty/ordinary_test.go", testSource("", "TestOrdinary"))

	for _, ignored := range []string{"testdata", "vendor", ".hidden", "_hidden", "nested"} {
		writeFixture(t, root, "alpha/"+ignored+"/broken_test.go", "not Go source")
	}

	writeFixture(t, root, "alpha/nested/go.mod", "module nested.example\n")
	var output bytes.Buffer
	err := testselection.Run(
		root,
		[]string{"-tags", "integration", "./zeta/...", "./alpha/...", "./alpha"},
		&output,
		build.Default,
	)
	if err != nil {
		t.Fatal(err)
	}

	want := "./alpha\t^(TestAlpha|TestBeta|TestEngineChild)$\n" +
		"./alpha/child\t^(TestChild)$\n" +
		"./zeta\t^(TestZeta)$\n"
	if output.String() != want {
		t.Fatalf("selection output = %q, want %q", output.String(), want)
	}
}

func TestRunUsesPlatformAndCgoConstraints(t *testing.T) {
	root := moduleFixture(t)
	writeFixture(
		t,
		root,
		"sample/tagged_linux_amd64_test.go",
		testSource("integration", "TestLinux"),
	)
	writeFixture(
		t,
		root,
		"sample/tagged_darwin_arm64_test.go",
		testSource("integration", "TestMac"),
	)
	writeFixture(t, root, "sample/cgo_test.go", testSource("integration && cgo", "TestCgo"))
	writeFixture(t, root, "sample/extra_test.go", testSource("integration && extra", "TestExtra"))

	for _, scenario := range []struct {
		name, goos, goarch, tags, expression string
		cgo                                  bool
	}{
		{"linux cgo", "linux", "amd64", "integration", "^(TestCgo|TestLinux)$", true},
		{"linux without cgo", "linux", "amd64", "integration", "^(TestLinux)$", false},
		{"mac", "darwin", "arm64", "integration", "^(TestMac)$", false},
		{"extra tag", "linux", "amd64", "integration,extra", "^(TestExtra|TestLinux)$", false},
	} {
		t.Run(
			scenario.name,
			func(t *testing.T) {
				context := build.Default
				context.GOOS, context.GOARCH, context.CgoEnabled = scenario.goos, scenario.goarch, scenario.cgo
				var output bytes.Buffer
				err := testselection.Run(
					root,
					[]string{"-tags", scenario.tags, "./sample"},
					&output,
					context,
				)
				if err != nil {
					t.Fatal(err)
				}

				want := "./sample\t" + scenario.expression + "\n"
				if output.String() != want {
					t.Fatalf("selection output = %q, want %q", output.String(), want)
				}
			},
		)
	}
}

func TestRunOmitsPackagesWithoutAdditionalTests(t *testing.T) {
	root := moduleFixture(t)
	writeFixture(t, root, "sample/ordinary_test.go", testSource("", "TestOrdinary"))
	writeFixture(t, root, "sample/other_test.go", testSource("other", "TestOther"))
	var output bytes.Buffer
	if err := testselection.Run(
		root,
		[]string{"-tags", "integration", "./sample/..."},
		&output,
		build.Default,
	); err != nil {
		t.Fatal(err)
	}

	if output.Len() != 0 {
		t.Fatalf("ordinary tests produced selection records: %q", output.String())
	}
}

func moduleFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "go.mod", "module fixture.example\n")

	return root
}

func writeFixture(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testSource(tags string, names ...string) string {
	var source bytes.Buffer
	if tags != "" {
		source.WriteString("//go:build " + tags + "\n\n")
	}

	source.WriteString("package sample\n\nimport \"testing\"\n\n")

	for _, name := range names {
		if name == "TestMain" {
			source.WriteString("func TestMain(m *testing.M) {}\n")

			continue
		}

		source.WriteString("func " + name + "(t *testing.T) {}\n")
	}

	return source.String()
}
