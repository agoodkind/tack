package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// verbsFilePrefix starts the name of every file in this package that declares
// verbs: verbs.go and the verbs_<family>.go files.
const verbsFilePrefix = "verbs"

// isVerbsFile reports whether name is a non-test Go file of this package that
// declares verbs.
func isVerbsFile(name string) bool {
	return strings.HasPrefix(name, verbsFilePrefix) && strings.HasSuffix(name, ".go") &&
		!strings.HasSuffix(name, "_test.go")
}

// parsedVerbsFiles parses every verbs file of this package. The test fails
// when verbs.go is absent from the set.
func parsedVerbsFiles(t *testing.T) []*ast.File {
	t.Helper()
	matches, err := filepath.Glob(verbsFilePrefix + "*.go")
	if err != nil {
		t.Fatalf("list the verbs files: %v", err)
	}
	names := make([]string, 0, len(matches))
	for _, name := range matches {
		if isVerbsFile(name) {
			names = append(names, name)
		}
	}
	if !slices.Contains(names, "verbs.go") {
		t.Fatalf("verbs files = %v, want verbs.go among them: the glob is wrong", names)
	}
	fileSet := token.NewFileSet()
	parsed := make([]*ast.File, 0, len(names))
	for _, name := range names {
		file, err := parser.ParseFile(fileSet, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		parsed = append(parsed, file)
	}
	return parsed
}

// declaredVerbs parses every verbs file and returns constant name to verb
// value for every declared Verb. Parsing finds a renamed or reformatted
// constant that a text scan would miss.
func declaredVerbs(t *testing.T) map[string]Verb {
	t.Helper()
	declared := map[string]Verb{}
	for _, parsed := range parsedVerbsFiles(t) {
		for _, decl := range parsed.Decls {
			general, ok := decl.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, spec := range general.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) != len(value.Values) {
					continue
				}
				for index, name := range value.Names {
					literal, ok := value.Values[index].(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					declared[name.Name] = Verb(strings.Trim(literal.Value, `"`))
				}
			}
		}
	}
	return declared
}

// collectVerbMapEntries marks as referenced every verb name in a tool routing
// map of any verbs file. The MCP wrapper records the verbs of those maps.
func collectVerbMapEntries(t *testing.T, referenced map[string]bool) {
	t.Helper()
	found := false
	for _, parsed := range parsedVerbsFiles(t) {
		for _, decl := range parsed.Decls {
			general, ok := decl.(*ast.GenDecl)
			if !ok || general.Tok != token.VAR {
				continue
			}
			for _, spec := range general.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) != 1 || !toolRoutingMaps[value.Names[0].Name] {
					continue
				}
				found = true
				ast.Inspect(value, func(n ast.Node) bool {
					ident, ok := n.(*ast.Ident)
					if ok && strings.HasPrefix(ident.Name, "Verb") {
						referenced[ident.Name] = true
					}
					return true
				})
			}
		}
	}
	if !found {
		t.Fatalf("found none of the tool routing maps %v in the verbs files: they were renamed and this test now proves nothing",
			toolRoutingMaps)
	}
}
