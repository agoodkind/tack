package datagen

import (
	"reflect"
	"testing"

	"goodkind.io/tack/internal/config"
)

func TestPlanIdentitiesKeepsDataDeterministicWithoutMintingTokens(t *testing.T) {
	t.Parallel()
	scale, err := ParseScale("small")
	if err != nil {
		t.Fatalf("ParseScale() error = %v", err)
	}
	cfg := &config.Config{Env: "production"}
	first := PlanIdentities(cfg, 245, scale)
	second := PlanIdentities(cfg, 245, scale)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("PlanIdentities() data changed between runs")
	}
	for _, workspace := range first.Workspaces {
		for _, actor := range workspace.Actors {
			if actor.RawToken != "" {
				t.Fatalf("dry-run actor %s minted raw token", actor.Email)
			}
		}
	}
}

func TestMintTokensChangesTokensWithoutChangingIdentityData(t *testing.T) {
	t.Parallel()
	scale, err := ParseScale("small")
	if err != nil {
		t.Fatalf("ParseScale() error = %v", err)
	}
	cfg := &config.Config{Env: "production"}
	first := PlanIdentities(cfg, 245, scale)
	second := PlanIdentities(cfg, 245, scale)
	if err := mintTokens(&first, cfg); err != nil {
		t.Fatalf("mintTokens(first) error = %v", err)
	}
	if err := mintTokens(&second, cfg); err != nil {
		t.Fatalf("mintTokens(second) error = %v", err)
	}
	if first.Workspaces[0].Actors[0].RawToken == second.Workspaces[0].Actors[0].RawToken {
		t.Fatal("mintTokens() reused actor raw token")
	}
	if first.ExpiredToken == second.ExpiredToken || first.BogusToken == second.BogusToken {
		t.Fatal("mintTokens() reused rejected token")
	}
	firstTokens := clearIdentityTokens(first)
	secondTokens := clearIdentityTokens(second)
	if !reflect.DeepEqual(firstTokens, secondTokens) {
		t.Fatal("mintTokens() changed deterministic identity data")
	}
}

func clearIdentityTokens(identities Identities) Identities {
	identities.ExpiredToken = ""
	identities.ExpiredRequestToken = ""
	identities.BogusToken = ""
	identities.BogusRequestToken = ""
	for workspaceIndex := range identities.Workspaces {
		for actorIndex := range identities.Workspaces[workspaceIndex].Actors {
			actor := &identities.Workspaces[workspaceIndex].Actors[actorIndex]
			actor.Token = ""
			actor.RawToken = ""
			actor.RequestToken = ""
		}
	}
	return identities
}
