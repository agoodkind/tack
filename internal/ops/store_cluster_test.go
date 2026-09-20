package ops

import (
	"strings"
	"testing"
)

// The coordinator list becomes a word list in an fdbcli command that replaces
// the cluster's coordinators. A wrong entry there points every client at an
// address that answers nothing, so these are the refusals that keep the
// migration from naming one.

func TestParseCoordinatorAddressesBracketsThePinnedGuestAddresses(t *testing.T) {
	addresses, err := parseCoordinatorAddresses(
		"3d06:bad:b01:210::220, 3d06:bad:b01:210::221 ,3d06:bad:b01:210::222")
	if err != nil {
		t.Fatalf("parseCoordinatorAddresses: %v", err)
	}
	want := "[3d06:bad:b01:210::220]:4500 [3d06:bad:b01:210::221]:4500 [3d06:bad:b01:210::222]:4500"
	if got := strings.Join(addresses, " "); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestParseCoordinatorAddressesKeepsAnExplicitPort(t *testing.T) {
	addresses, err := parseCoordinatorAddresses("[3d06:bad:b01:210::220]:4501")
	if err != nil {
		t.Fatalf("parseCoordinatorAddresses: %v", err)
	}
	if addresses[0] != "[3d06:bad:b01:210::220]:4501" {
		t.Fatalf("got %q, want the port that was given", addresses[0])
	}
}

func TestParseCoordinatorAddressesRefusesAName(t *testing.T) {
	_, err := parseCoordinatorAddresses("fdb")
	if err == nil {
		t.Fatal("a name was accepted; site DNS answers every name as the proxy")
	}
	if !strings.Contains(err.Error(), "pinned addresses") {
		t.Fatalf("the refusal said %q, which does not say why a name is refused", err)
	}
}

func TestParseCoordinatorAddressesRefusesAnEvenCount(t *testing.T) {
	_, err := parseCoordinatorAddresses("3d06:bad:b01:210::220,3d06:bad:b01:210::221")
	if err == nil {
		t.Fatal("an even coordinator count was accepted; a quorum needs an odd one")
	}
}

func TestParseCoordinatorAddressesRefusesAnEmptyList(t *testing.T) {
	if _, err := parseCoordinatorAddresses("  ,  "); err == nil {
		t.Fatal("an empty list was accepted")
	}
}

func TestValidateRedundancyModeAcceptsOnlyTheThreeModes(t *testing.T) {
	for _, mode := range []string{"single", "double", "triple"} {
		if err := validateRedundancyMode(mode); err != nil {
			t.Fatalf("validateRedundancyMode(%q): %v", mode, err)
		}
	}
	for _, mode := range []string{"", "double ssd", "new double", "DOUBLE"} {
		if err := validateRedundancyMode(mode); err == nil {
			t.Fatalf("validateRedundancyMode(%q) accepted a word that reaches fdbcli as a command", mode)
		}
	}
}

func TestParseExcludeAddressesAcceptsAnEvenCount(t *testing.T) {
	addresses, err := parseExcludeAddresses("3d06:bad:b01:210::217,3d06:bad:b01:210:7ac::b")
	if err != nil {
		t.Fatalf("parseExcludeAddresses: %v", err)
	}
	if len(addresses) != 2 {
		t.Fatalf("got %d addresses, want 2", len(addresses))
	}
}
