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

// unemittedVerbs names every declared verb that nothing records yet, and says
// why. A verb may sit here only while the feature that would emit it is
// unbuilt, and every entry names the ticket that will build it.
//
// This map is the escape hatch, so it is deliberately awkward: a verb added to
// verbs.go with no emitter fails TestEveryDeclaredVerbHasAnEmitter until
// someone writes a reason here, and a verb whose emitter is later deleted
// fails the same way. Silence about a missing emitter is the thing this file
// exists to prevent (TACK-340).
var unemittedVerbs = map[Verb]string{
	VerbPropertyDefDelete:  "no property-def deletion tool exists; TACK-340 tracks the feature",
	VerbOrgMemberRemove:    "no membership removal mutation exists; TACK-340 tracks the feature",
	VerbOrgMemberRoleSet:   "no role mutation exists; TACK-340 tracks the feature",
	VerbAuthLoginSucceeded: "there is no login endpoint; bearer auth emits auth.token_used instead; TACK-340",
	VerbAuditNotarized:     "the notarizer writes its own table row and records no ledger event; TACK-340",
	VerbAuditChainVerified: "bundle verification is offline with no operator actor; TACK-340",
	VerbAuditDropped:       "a recorder that cannot record cannot record its own failure; TACK-320 owns the outage log",
}

// TestEveryDeclaredVerbHasAnEmitter walks the repository and fails when a
// declared verb is recorded nowhere.
//
// The tool-to-verb test in coverage_test.go checks the other direction: that a
// registered tool resolves to a verb. Neither direction implies the other, and
// only this one catches a verb whose emitter was never written, which is how
// the TACK-340 audit found eleven verbs that nothing emitted and one whose
// ticket recorded it as shipped when it had never been wired.
//
// What this test does not catch, stated so nobody reads more into a pass than
// it earns: an emitter that exists and is never called still names its verb
// here, so this stays green. Two other gates cover that half. The deadcode
// lint fails an unreachable function, which is what an orphaned emitter
// becomes, and each emitter's own test drives the handler rather than the
// helper, so a handler that stops recording fails there. All three were
// checked by mutation rather than assumed.
func TestEveryDeclaredVerbHasAnEmitter(t *testing.T) {
	declared := declaredVerbs(t)
	if len(declared) < 50 {
		t.Fatalf("found %d declared verbs, want the whole taxonomy: the parse is wrong", len(declared))
	}
	referenced := verbNamesReferencedOutsideDeclarations(t)

	for name, verb := range declared {
		if referenced[name] {
			if reason, exempt := unemittedVerbs[verb]; exempt {
				t.Errorf("verb %s (%q) is recorded somewhere but still listed as unemitted (%q): delete the entry",
					name, verb, reason)
			}
			continue
		}
		if _, exempt := unemittedVerbs[verb]; exempt {
			continue
		}
		t.Errorf("verb %s (%q) is declared but nothing records it: wire an emitter, delete the verb, "+
			"or add it to unemittedVerbs with the ticket that will build the feature", name, verb)
	}
}

// TestUnemittedVerbsAreAllDeclared keeps the escape hatch honest in the other
// direction: an entry for a verb that no longer exists is a stale exemption
// that would silently cover a future verb of the same name.
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

// declaredVerbs parses verbs.go and returns constant name to verb value for
// every declared Verb. Parsing rather than scanning text means a renamed or
// reformatted constant is still found.
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

// verbNamesReferencedOutsideDeclarations returns every verb constant name used
// anywhere that is not its own declaration.
//
// Two sources count as recording the verb. A reference in non-test code
// outside this package's verbs.go is an emitter or a path to one. A reference
// inside verbs.go that is not a declaration is a map entry, which is how the
// MCP tool verbs reach the wrapper that records them; that wrapper is the
// emitter for all of them, so the map entry is the honest evidence.
//
// Test files are excluded deliberately: a verb that only appears in a test is
// a verb nothing in production records, which is exactly the case to catch.
func verbNamesReferencedOutsideDeclarations(t *testing.T) map[string]bool {
	t.Helper()
	referenced := map[string]bool{}
	collectVerbMapEntries(t, referenced)

	root := filepath.Join("..", "..")
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if filepath.Base(path) == "verbs.go" && strings.Contains(path, filepath.Join("internal", "audit")) {
			return nil
		}
		for _, name := range verbIdentifiers(t, path) {
			referenced[name] = true
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk the repository: %v", walkErr)
	}
	return referenced
}

// toolRoutingMaps are the variables in verbs.go whose entries put a verb on a
// path that records it: ToolVerb reads both, and the MCP wrapper records
// whatever they return.
//
// stateChangeVerbs is deliberately not here. It classifies a verb as
// synchronous rather than routing it anywhere, so counting its entries would
// mark five verbs as emitted that nothing emits. The first run of this test
// did exactly that, which is the whole argument for keeping the two kinds of
// map apart.
var toolRoutingMaps = map[string]bool{
	"staticToolVerb":    true,
	"perTypePrefixVerb": true,
}

// collectVerbMapEntries records verb names that verbs.go routes to the MCP
// wrapper, which is their emitter.
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
			ast.Inspect(value, func(n ast.Node) bool {
				ident, ok := n.(*ast.Ident)
				if ok && strings.HasPrefix(ident.Name, "Verb") {
					referenced[ident.Name] = true
				}
				return true
			})
		}
	}
	if !found {
		t.Fatalf("found none of the tool routing maps %v in verbs.go: they were renamed and this test now proves nothing",
			toolRoutingMaps)
	}
}

// verbIdentifiers returns the Verb-prefixed identifiers a file actually uses.
//
// It parses rather than scans text. A textual scan was the first version and
// it was wrong in a way that mattered: a verb named in a comment or a string
// counted as emitted, so a verb could be documented into coverage it did not
// have. Parsing sees identifiers only, so prose about a verb no longer stands
// in for code that records it.
func verbIdentifiers(t *testing.T, path string) []string {
	t.Helper()
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, path, nil, 0)
	if err != nil {
		// A file the compiler accepts must parse here too, so a failure means
		// this test is walking something it should not, and skipping it would
		// silently shrink the search.
		t.Fatalf("parse %s: %v", path, err)
	}
	var names []string
	ast.Inspect(parsed, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if ok && strings.HasPrefix(ident.Name, "Verb") && len(ident.Name) > len("Verb") {
			names = append(names, ident.Name)
		}
		return true
	})
	return names
}
