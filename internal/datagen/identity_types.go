package datagen

import (
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/config"
)

// Actor is one real SQL identity used to authenticate generated requests.
type Actor struct {
	UserID       uuid.UUID
	Email        string
	DisplayName  string
	Token        string
	RawToken     string
	RequestToken string
}

// WorkspaceIdentity is one bootstrapped MCP entry point and its actors.
type WorkspaceIdentity struct {
	OrgID   uuid.UUID
	OrgSlug string
	Slug    string
	Name    string
	Actors  []Actor
}

// Identities contains every request identity and negative-auth token.
type Identities struct {
	Workspaces          []WorkspaceIdentity
	ExpiredToken        string
	ExpiredRequestToken string
	BogusToken          string
	BogusRequestToken   string
}

func plannedIdentities(cfg *config.Config, seed int64, scale Scale) Identities {
	identities := Identities{
		Workspaces:          make([]WorkspaceIdentity, 0, scale.OrgCount*scale.WorkspacesPerOrg),
		ExpiredToken:        deterministicTokenFor(seed, "expired"),
		ExpiredRequestToken: deterministicTokenFor(seed, "expired"),
		BogusToken:          deterministicTokenFor(seed, "bogus-not-stored"),
		BogusRequestToken:   deterministicTokenFor(seed, "bogus-not-stored"),
	}
	for orgIndex := range scale.OrgCount {
		orgSlug := fmt.Sprintf("qa-%d-org-%02d", seed, orgIndex+1)
		orgID := deterministicUUIDv7(orgSlug)
		actors := plannedActors(cfg, seed, orgIndex, scale.ActorsPerOrg)
		for workspaceIndex := range scale.WorkspacesPerOrg {
			slug := fmt.Sprintf("qa-%d-o%02d-w%02d", seed, orgIndex+1, workspaceIndex+1)
			identities.Workspaces = append(identities.Workspaces, WorkspaceIdentity{
				OrgID: orgID, OrgSlug: orgSlug, Slug: slug,
				Name:   fmt.Sprintf("QA Workspace %d.%d seed %d", orgIndex+1, workspaceIndex+1, seed),
				Actors: append([]Actor(nil), actors...),
			})
		}
	}
	return identities
}

func plannedActors(cfg *config.Config, seed int64, orgIndex, count int) []Actor {
	actors := make([]Actor, 0, count)
	for actorIndex := range count {
		email := fmt.Sprintf(
			"qa-datagen-%d-o%02d-u%02d@example.invalid",
			seed,
			orgIndex+1,
			actorIndex+1,
		)
		userID := deterministicUUID(email)
		requestToken := deterministicTokenFor(seed, email)
		token := requestToken
		if cfg.Env == "development" {
			token = userID.String()
			requestToken = token
		}
		actors = append(actors, Actor{
			UserID: userID, Email: email,
			DisplayName:  fmt.Sprintf("QA Actor %d.%d", orgIndex+1, actorIndex+1),
			Token:        token,
			RequestToken: requestToken,
		})
	}
	return actors
}
