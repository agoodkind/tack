package integration_test

import (
	"crypto/rand"
	"encoding/hex"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/test/integration"
)

const (
	seedlessMemberRole  = 20
	seedlessTokenBytes  = 24
	seedlessEntryName   = "Seedless entry"
	seedlessRootName    = "Seedless organization"
	seedlessTokenLabel  = "seedless-search"
	seedlessEmailDomain = "@example.invalid"
)

type seedlessOrg struct {
	OrgID        uuid.UUID
	EntryID      uuid.UUID
	EntryAddress string
	UserID       uuid.UUID
	Token        string
	Keys         seedlessKeys
	Nodes        map[string]uuid.UUID
}

// putSeedlessOrg writes the organization root and entry node through the
// store. Both types exclude generic MCP tools. It creates the caller,
// membership, and API token through SQL repositories, then checks that the
// organization stores only the declared node types and property definitions.
func putSeedlessOrg(t *testing.T, env seedlessEnvironment, keys seedlessKeys) seedlessOrg {
	t.Helper()
	built := seedlessOrg{
		OrgID: uuid.Must(uuid.NewV7()), EntryID: uuid.Must(uuid.NewV7()),
		EntryAddress: opaqueSeedlessKey("e"), UserID: uuid.Nil, Token: "", Keys: keys,
		Nodes: map[string]uuid.UUID{},
	}
	putSeedlessMetadata(t, env, built.OrgID, keys)
	root := newSeedlessHierarchyNode(built.OrgID, built.OrgID, keys.Root, seedlessRootName, keys.Address, opaqueSeedlessKey("r"))
	putSeedlessHierarchyNode(t, env, root, uuid.Nil, []string{keys.Address})
	entry := newSeedlessHierarchyNode(built.OrgID, built.EntryID, keys.Entry, seedlessEntryName, keys.Address, built.EntryAddress)
	putSeedlessHierarchyNode(t, env, entry, built.OrgID, []string{keys.Address, seedlessParentProperty})
	built.UserID, built.Token = putSeedlessIdentity(t, env, built.OrgID)
	requireOnlyDeclaredMetadata(t, env, built)
	return built
}

func putSeedlessIdentity(t *testing.T, env seedlessEnvironment, orgID uuid.UUID) (uuid.UUID, string) {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	created, err := postgres.NewUserRepo(env.Pool).Create(ctx, &user.User{
		ID: uuid.Must(uuid.NewV7()), Email: "seedless-" + uuid.NewString() + seedlessEmailDomain,
		DisplayName: "Seedless caller", AvatarURL: nil, CreatedAt: time.Time{}, UpdatedAt: time.Time{},
	})
	if err != nil {
		t.Fatalf("create the caller of organization %s: %v", orgID, err)
	}
	membership := &org.Member{ID: uuid.Nil, OrgID: orgID, UserID: created.ID, Role: seedlessMemberRole, CreatedAt: time.Time{}}
	if err := postgres.NewOrgMemberRepo(env.Pool).AddMember(ctx, membership); err != nil {
		t.Fatalf("add the caller to organization %s: %v", orgID, err)
	}
	entropy := make([]byte, seedlessTokenBytes)
	if _, err := rand.Read(entropy); err != nil {
		t.Fatalf("generate the caller token: %v", err)
	}
	token := "tack_seedless_" + hex.EncodeToString(entropy)
	if _, err := postgres.NewTokenRepo(env.Pool).Create(ctx, orgID, created.ID, token, seedlessTokenLabel); err != nil {
		t.Fatalf("create the caller token in organization %s: %v", orgID, err)
	}
	return created.ID, token
}

func requireOnlyDeclaredMetadata(t *testing.T, env seedlessEnvironment, built seedlessOrg) {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	types, err := env.Stores.NodeTypes.List(ctx, built.OrgID)
	if err != nil {
		t.Fatalf("list node types of organization %s: %v", built.OrgID, err)
	}
	typeKeys := make([]string, 0, len(types))
	for _, kind := range types {
		typeKeys = append(typeKeys, kind.TypeKey)
	}
	keys := built.Keys
	requireSameNames(t, "node types", built.OrgID, typeKeys, []string{keys.Root, keys.Entry, keys.Container, keys.Nested})
	definitions, err := env.Stores.PropertyDefs.List(ctx, built.OrgID)
	if err != nil {
		t.Fatalf("list property definitions of organization %s: %v", built.OrgID, err)
	}
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	requireSameNames(t, "property definitions", built.OrgID, names, []string{
		keys.Address, seedlessParentProperty, seedlessScopeProperty,
		keys.Included, keys.Excluded, keys.Default, keys.Choice,
	})
}

func requireSameNames(t *testing.T, kind string, orgID uuid.UUID, got, want []string) {
	t.Helper()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("organization %s stores %s %v, want only %v", orgID, kind, got, want)
	}
}
