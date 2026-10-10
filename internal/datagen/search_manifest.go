package datagen

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/config"
)

const (
	approvedSearchCorpusSHA256 = "f8d11433cdda164be34425e155184330bb5e56c95d9d0d341e8bfc6f80f6188f"
	embeddedCorpusSource       = "embedded"
	suppliedCorpusSource       = "supplied"
)

//go:embed search_semantic_corpus.json
var approvedSearchCorpusJSON []byte

// SearchManifest exports stored QA fixture identities without credentials.
type SearchManifest struct {
	OrgID              uuid.UUID                `json:"org_id"`
	WorkspaceReference string                   `json:"workspace_reference"`
	ActorEmail         string                   `json:"actor_email"`
	CorpusSHA256       string                   `json:"corpus_sha256"`
	Nodes              []SearchManifestNode     `json:"nodes"`
	Cases              []SearchManifestCase     `json:"cases"`
	Types              []SearchManifestType     `json:"types"`
	Properties         []SearchManifestProperty `json:"properties"`
}

// SearchManifestType records a created opaque node-type definition.
type SearchManifestType struct {
	ID  uuid.UUID `json:"id"`
	Key string    `json:"key"`
}

// SearchManifestProperty records a created projection definition.
type SearchManifestProperty struct {
	ID       uuid.UUID `json:"id"`
	Key      string    `json:"key"`
	Included bool      `json:"included"`
}

// SearchManifestNode records one created node and its projection declarations.
type SearchManifestNode struct {
	ID          uuid.UUID `json:"id"`
	Identifier  string    `json:"identifier"`
	NodeType    string    `json:"node_type"`
	IncludedKey string    `json:"included_key"`
	ExcludedKey string    `json:"excluded_key"`
	TextBytes   int       `json:"text_bytes"`
	Name        string    `json:"name"`
}

// SearchManifestCase declares either exact traversal IDs or ranked targets.
type SearchManifestCase struct {
	Query       string      `json:"query"`
	NodeType    string      `json:"node_type"`
	ExpectedIDs []uuid.UUID `json:"expected_ids"`
	Match       string      `json:"match"`
	RankLimit   int         `json:"rank_limit"`
}

type searchManifestSource struct {
	Identifier string `json:"identifier"`
	Text       string `json:"text"`
}

// PrepareSearchManifest writes a bounded fixture under an existing seed workspace.
// It permits disabled public search, records search work for the prepared
// nodes, and starts no worker.
// A nil `corpus` selects the embedded approved corpus.
func PrepareSearchManifest(ctx context.Context, cfg *config.Config, seed int64, corpus []byte) (SearchManifest, error) {
	if err := ValidateTarget(cfg); err != nil {
		return SearchManifest{}, err
	}
	rows, err := loadApprovedSearchCorpus(ctx, corpus)
	if err != nil {
		return SearchManifest{}, err
	}
	scale, err := ParseScale(searchVerificationScale)
	if err != nil {
		return SearchManifest{}, err
	}
	workspace := PlanIdentities(cfg, seed, scale).Workspaces[0]
	stores, err := fdbadapter.NewStores(cfg.FDBClusterFile, cfg.FDBTransactionTimeout, nil)
	if err != nil {
		return SearchManifest{}, loggedError(ctx, "qa datagen: open fixture stores", err)
	}
	stores.EnableSearchWork()
	manifest := SearchManifest{
		OrgID: workspace.OrgID, WorkspaceReference: workspace.Slug,
		ActorEmail: workspace.Actors[0].Email, CorpusSHA256: approvedSearchCorpusSHA256,
		Nodes:      make([]SearchManifestNode, 0, len(rows)+searchContinuationNodes+2),
		Cases:      make([]SearchManifestCase, 0, 8),
		Types:      make([]SearchManifestType, 0, 3),
		Properties: make([]SearchManifestProperty, 0, 6),
	}
	if err := prepareSemanticManifest(ctx, stores, workspace, rows, &manifest); err != nil {
		return manifest, err
	}
	if err := prepareTraversalManifest(ctx, stores, workspace, &manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func loadApprovedSearchCorpus(ctx context.Context, corpus []byte) ([]searchManifestSource, error) {
	contents, source := approvedSearchCorpusJSON, embeddedCorpusSource
	if corpus != nil {
		contents, source = corpus, suppliedCorpusSource
	}
	digest := sha256.Sum256(contents)
	if hex.EncodeToString(digest[:]) != approvedSearchCorpusSHA256 {
		return nil, fmt.Errorf("qa datagen: semantic corpus %q does not match approved SHA256", source)
	}
	var rows []searchManifestSource
	if err := json.Unmarshal(contents, &rows); err != nil {
		return nil, loggedError(ctx, fmt.Sprintf("qa datagen: decode semantic corpus %q", source), err)
	}
	return rows, nil
}

func prepareSemanticManifest(ctx context.Context, stores *fdbadapter.Stores, workspace WorkspaceIdentity, rows []searchManifestSource, manifest *SearchManifest) error {
	fixture, err := newSearchFixtureWithManifest(ctx, stores, workspace, manifest)
	if err != nil {
		return err
	}
	queries := []string{"db", "signin", "lag", "invoice", "crash", "remove user"}
	targets := make([][]uuid.UUID, len(queries))
	for _, row := range rows {
		nodeID, err := fixture.putManifestNode(ctx, row.Identifier, row.Text, row.Text, manifest)
		if err != nil {
			return err
		}
		if strings.HasPrefix(row.Identifier, "t-") {
			position := int(row.Identifier[2] - '0')
			targets[position] = append(targets[position], nodeID)
		}
	}
	for position, query := range queries {
		manifest.Cases = append(manifest.Cases, SearchManifestCase{
			Query: query, NodeType: fixture.typeKey, ExpectedIDs: targets[position], Match: "ranked_targets", RankLimit: 25,
		})
	}
	return nil
}
