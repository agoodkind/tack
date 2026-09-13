package ops

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/tack/internal/config"
)

// savedLauncherConfig is a saved yugabyted.conf in the shape the launcher on
// QA data1 wrote on 2026-09-13, cut down to the keys that matter here plus a
// few neighbours, so the rewrite is exercised against the real layout.
const savedLauncherConfig = `{
    "advertise_address": "yb1",
    "callhome": true,
    "current_masters": "yugabyte:7100,yb1:7100,yb2:7100",
    "data_dir": "/home/yugabyte/var/data",
    "join": "yb2",
    "master_rpc_port": 7100,
    "polling_interval": "5"
}
`

func TestDeployedLedgerNodeNamesReadsTheHostMap(t *testing.T) {
	tests := []struct {
		hosts string
		want  []string
		fails bool
	}{
		{hosts: "yb1=3d06:bad:b01::120,yb2=3d06:bad:b01::121,yb3=3d06:bad:b01::122", want: []string{"yb1", "yb2", "yb3"}},
		{hosts: " yb3=::3 , yb1=::1 ,, yb1=::1 ", want: []string{"yb1", "yb3"}},
		{hosts: "", fails: true},
		{hosts: "yb1", fails: true},
		{hosts: "=3d06:bad:b01::120", fails: true},
	}
	for _, test := range tests {
		got, err := deployedLedgerNodeNames(&config.Config{LedgerNodeHosts: test.hosts})
		if test.fails {
			if err == nil {
				t.Errorf("deployedLedgerNodeNames(%q) = %v, want an error", test.hosts, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("deployedLedgerNodeNames(%q): %v", test.hosts, err)
			continue
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Errorf("deployedLedgerNodeNames(%q) = %v, want %v", test.hosts, got, test.want)
		}
	}
}

func TestSavedLedgerMastersReadsTheLauncherList(t *testing.T) {
	got, err := savedLedgerMasters(context.Background(), []byte(savedLauncherConfig))
	if err != nil {
		t.Fatalf("savedLedgerMasters: %v", err)
	}
	want := []string{"yb1:7100", "yb2:7100", "yugabyte:7100"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("saved masters = %v, want %v", got, want)
	}
	if _, err := savedLedgerMasters(context.Background(), []byte(`{"join": "yb2"}`)); err == nil {
		t.Fatal("a config without current_masters must be refused, not read as an empty list")
	}
	if _, err := savedLedgerMasters(context.Background(), []byte(`not json`)); err == nil {
		t.Fatal("a config that is not JSON must be refused")
	}
}

// TestRewriteLedgerMastersReplacesOnlyTheList is the repair the incident
// needed: the stale legacy entry leaves, the missing node arrives, and every
// other key the launcher wrote reads back exactly as it was.
func TestRewriteLedgerMastersReplacesOnlyTheList(t *testing.T) {
	names, err := deployedLedgerNodeNames(&config.Config{LedgerNodeHosts: "yb1=::1,yb2=::2,yb3=::3"})
	if err != nil {
		t.Fatalf("deployedLedgerNodeNames: %v", err)
	}
	wanted := ledgerMasterList(names)
	current, err := savedLedgerMasters(context.Background(), []byte(savedLauncherConfig))
	if err != nil {
		t.Fatalf("savedLedgerMasters: %v", err)
	}
	if equalStringSets(current, wanted) {
		t.Fatal("the fixture's stale list must differ from the deployed set")
	}

	rewritten, err := rewriteLedgerMasters(context.Background(), []byte(savedLauncherConfig), wanted)
	if err != nil {
		t.Fatalf("rewriteLedgerMasters: %v", err)
	}
	after, err := savedLedgerMasters(context.Background(), rewritten)
	if err != nil {
		t.Fatalf("savedLedgerMasters after rewrite: %v", err)
	}
	if !equalStringSets(after, wanted) {
		t.Fatalf("masters after rewrite = %v, want %v", after, wanted)
	}

	var before, afterFields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(savedLauncherConfig), &before); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if err := json.Unmarshal(rewritten, &afterFields); err != nil {
		t.Fatalf("parse rewrite: %v", err)
	}
	for key, value := range before {
		if key == ledgerLauncherMastersKey {
			continue
		}
		if string(afterFields[key]) != string(value) {
			t.Errorf("key %s changed from %s to %s", key, value, afterFields[key])
		}
	}
	if len(afterFields) != len(before) {
		t.Fatalf("rewrite has %d keys, want %d", len(afterFields), len(before))
	}
	if !strings.HasSuffix(string(rewritten), "}\n") {
		t.Fatal("the rewritten file must end with a newline like the launcher's own")
	}
}

func TestLedgerMasterListIsOrderIndependent(t *testing.T) {
	a := ledgerMasterList([]string{"yb3", "yb1", "yb2"})
	b := ledgerMasterList([]string{"yb1", "yb2", "yb3"})
	if !equalStringSets(a, b) {
		t.Fatalf("%v and %v must compare equal", a, b)
	}
	if equalStringSets(a, ledgerMasterList([]string{"yb1", "yb2"})) {
		t.Fatal("a shorter list must not compare equal")
	}
}
