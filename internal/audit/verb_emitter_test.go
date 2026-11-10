package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// unemittedVerbs lists declared verbs without production references and gives
// their reasons. Each exemption must identify the ticket for its unbuilt
// feature. A missing emitter or a later emitter removal fails the reference
// test unless the map supplies a reason (TACK-340).
//
// These exemptions affect the static reference test. They do not waive the
// runtime audit completeness contract.
var unemittedVerbs = map[Verb]string{
	VerbAuthLoginSucceeded: "there is no login endpoint; bearer auth emits auth.token_used instead; TACK-340",
	VerbAuditNotarized:     "the notarizer writes its own table row and records no ledger event; TACK-340",
	VerbAuditChainVerified: "bundle verification is offline with no operator actor; TACK-340",
	VerbAuditDropped:       "a recorder that cannot record cannot record its own failure; TACK-320 owns the outage log",
}

// TestEveryDeclaredVerbHasAnEmitter checks declared verbs for production
// identifier references or tool routing entries, except explicit exemptions.
//
// The tool coverage test checks listed tools for verb mappings. This test
// checks the reverse direction. The TACK-340 audit reported eleven verbs
// without emitters and one feature marked shipped without an emitter.
//
// A reference does not prove that an emitter runs or records an event. The
// original validation account reports that deadcode rejected unreachable
// functions and handler-level emitter tests detected missing recording. It
// also reports mutation checks of all three checks. This static test alone
// cannot establish those checks or complete runtime audit coverage.
func TestEveryDeclaredVerbHasAnEmitter(t *testing.T) {
	declared := declaredVerbs(t)
	if len(declared) < 50 {
		t.Fatalf(
			"found %d declared verbs, want the whole taxonomy: the parse is wrong",
			len(declared),
		)
	}

	referenced := verbNamesReferencedOutsideDeclarations(t)

	for name, verb := range declared {
		if referenced[name] {
			if reason, exempt := unemittedVerbs[verb]; exempt {
				t.Errorf(
					"verb %s (%q) is recorded somewhere but still listed as unemitted (%q): delete the entry",
					name,
					verb,
					reason,
				)
			}

			continue
		}

		if _, exempt := unemittedVerbs[verb]; exempt {
			continue
		}

		t.Errorf(
			"verb %s (%q) is declared but nothing records it: wire an emitter, delete the verb, "+
				"or add it to unemittedVerbs with the ticket that will build the feature",
			name,
			verb,
		)
	}
}

// TestUnemittedVerbsAreAllDeclared rejects exemptions for verbs that the
// declarations do not contain. A stale exemption could otherwise exempt a
// future verb with the same value.
func TestUnemittedVerbsAreAllDeclared(t *testing.T) {
	declared := declaredVerbs(t)

	values := make(map[Verb]bool, len(declared))
	for _, verb := range declared {
		values[verb] = true
	}

	for verb, reason := range unemittedVerbs {
		if !values[verb] {
			t.Errorf("unemittedVerbs lists %q (%q) which verbs.go no longer declares", verb, reason)
		}

		if reason == "" {
			t.Errorf("unemittedVerbs entry %q has no reason", verb)
		}
	}
}

// declaredVerbs parses the verb declarations and maps constant names to verb
// values. Parsing preserves detection after renaming or reformatting.
func declaredVerbs(t *testing.T) map[string]Verb {
	t.Helper()
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "verbs.go", nil, 0)
	if err != nil {
		t.Fatalf("parse verbs.go: %v", err)
	}

	declared := map[string]Verb{}

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

	return declared
}

// verbNamesReferencedOutsideDeclarations returns verb constant identifiers
// from non-test Go files outside the declaration file and from tool routing
// maps inside it.
//
// The test treats those references as evidence of an emitter. Tool routing
// entries select verbs for the MCP wrapper. Other identifier references do
// not prove that production executes an emitter.
//
// The helper excludes tests because a test-only reference does not establish
// production recording.
func verbNamesReferencedOutsideDeclarations(t *testing.T) map[string]bool {
	t.Helper()
	referenced := map[string]bool{}
	collectVerbMapEntries(t, referenced)

	root := filepath.Join("..", "..")
	walkErr := filepath.WalkDir(
		root,
		func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if entry.IsDir() {
				// Engine users own the data directories under .testenv. Entering those
				// directories can fail with permission denied.
				switch entry.Name() {
				case ".git", ".testenv", "node_modules":
					return filepath.SkipDir
				}

				return nil
			}

			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			if filepath.Base(path) == "verbs.go" &&
				strings.Contains(path, filepath.Join("internal", "audit")) {
				return nil
			}

			for _, name := range verbIdentifiers(t, path) {
				referenced[name] = true
			}

			return nil
		},
	)
	if walkErr != nil {
		t.Fatalf("read every Go file in the repository: %v", walkErr)
	}

	return referenced
}

// toolRoutingMaps selects the maps that [ToolVerb] reads to choose verbs for
// the MCP wrapper.
//
// stateChangeVerbs only classifies synchronous recording. Its entries do not
// select verbs for tools. The original test account reports that counting
// that map marked five verbs as emitted without emitters. The helper excludes
// it to distinguish classification from tool routing.
var toolRoutingMaps = map[string]bool{
	"staticToolVerb":    true,
	"perTypePrefixVerb": true,
}

// collectVerbMapEntries collects identifiers from the maps that select audit
// verbs for the MCP wrapper.
func collectVerbMapEntries(t *testing.T, referenced map[string]bool) {
	t.Helper()
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "verbs.go", nil, 0)
	if err != nil {
		t.Fatalf("parse verbs.go: %v", err)
	}

	found := false

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
			ast.Inspect(
				value,
				func(n ast.Node) bool {
					ident, ok := n.(*ast.Ident)
					if ok && strings.HasPrefix(ident.Name, "Verb") {
						referenced[ident.Name] = true
					}

					return true
				},
			)
		}
	}

	if !found {
		t.Fatalf(
			"found none of the tool routing maps %v in verbs.go: they were renamed and this test now proves nothing",
			toolRoutingMaps,
		)
	}
}

// verbIdentifiers returns Verb-prefixed Go identifiers from a parsed file.
//
// The first version scanned text and counted verb names in comments or
// strings as emitter references. Parsing excludes those prose occurrences
// from the reference check.
func verbIdentifiers(t *testing.T, path string) []string {
	t.Helper()
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		// A file that the compiler accepts must parse here. A failure can indicate
		// that this test selected an unintended file. The test fails instead of
		// skipping the file and reducing its search.
		t.Fatalf("parse %s: %v", path, err)
	}

	var names []string
	ast.Inspect(
		parsed,
		func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if ok && strings.HasPrefix(ident.Name, "Verb") && len(ident.Name) > len("Verb") {
				names = append(names, ident.Name)
			}

			return true
		},
	)

	return names
}
