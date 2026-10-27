package integration_test

import (
	"encoding/json"
	"testing"
)

const (
	seedlessExcludedText = "qzxv brumbleflax wendrigo"
	seedlessHolderName   = "Teal compass"
	seedlessControlName  = "Plain marker"
)

type seedlessChoice int

const (
	noChoice seedlessChoice = iota
	firstChoice
	secondChoice
)

type seedlessNodeSpec struct {
	Name     string
	Parent   string
	Included string
	Excluded string
	Default  string
	Choice   seedlessChoice
}

func seedlessNodeSpecs() []seedlessNodeSpec {
	return []seedlessNodeSpec{
		{Name: "Harbor lantern record", Parent: "", Included: "saffron pipeline granite harbor lantern meadow", Excluded: "", Default: "cobalt falcon juniper", Choice: firstChoice},
		{Name: "Granite meadow record", Parent: "", Included: "saffron pipeline granite harbor", Excluded: "", Default: "cobalt falcon", Choice: secondChoice},
		{Name: seedlessControlName, Parent: "", Included: seedlessExcludedText, Excluded: "", Default: "", Choice: noChoice},
		{Name: seedlessHolderName, Parent: "", Included: "teal compass bearing", Excluded: seedlessExcludedText, Default: "", Choice: noChoice},
		{Name: "Velvet glacier beacon", Parent: "", Included: "", Excluded: "", Default: "", Choice: noChoice},
		{Name: "Saffron nested entry", Parent: "Harbor lantern record", Included: "saffron pipeline granite", Excluded: "", Default: "cobalt", Choice: firstChoice},
		{Name: "Pipeline nested entry", Parent: "Granite meadow record", Included: "saffron pipeline", Excluded: "", Default: "", Choice: secondChoice},
		{Name: "Velvet glacier nested", Parent: "Harbor lantern record", Included: "", Excluded: "", Default: "", Choice: noChoice},
	}
}

func (s seedlessNodeSpec) properties(t *testing.T, keys seedlessKeys) json.RawMessage {
	t.Helper()
	properties := map[string]json.RawMessage{}
	values := map[string]string{keys.Included: s.Included, keys.Excluded: s.Excluded, keys.Default: s.Default}
	for key, value := range values {
		if value != "" {
			properties[key] = seedlessString(value)
		}
	}
	if s.Choice == firstChoice {
		properties[keys.Choice] = seedlessString(keys.FirstOption)
	}
	if s.Choice == secondChoice {
		properties[keys.Choice] = seedlessString(keys.SecondOption)
	}
	if len(properties) == 0 {
		return nil
	}
	encoded, err := json.Marshal(properties)
	if err != nil {
		t.Fatalf("encode the properties of %s: %v", s.Name, err)
	}
	return encoded
}

func putSeedlessCorpus(t *testing.T, env seedlessEnvironment, caller seedlessOrg) {
	t.Helper()
	keys := caller.Keys
	for _, spec := range seedlessNodeSpecs() {
		typeKey := keys.Container
		arguments := map[string]json.RawMessage{
			keys.Entry + "_reference": seedlessString(caller.EntryAddress), "name": seedlessString(spec.Name),
		}
		if spec.Parent != "" {
			parentID, found := caller.Nodes[spec.Parent]
			if !found {
				t.Fatalf("node %s needs parent %s, which the corpus did not create first", spec.Name, spec.Parent)
			}
			typeKey = keys.Nested
			arguments[keys.Container+"_reference"] = seedlessString(parentID.String())
		}
		if properties := spec.properties(t, keys); properties != nil {
			arguments["properties"] = properties
		}
		text := callSeedlessTool(t, env, caller, "tack_create_"+typeKey, arguments)
		caller.Nodes[spec.Name] = seedlessRawID(t, text)
	}
}
