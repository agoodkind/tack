package search

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// pageDocument is one active page with exactly the native mapping fields.
type pageDocument struct {
	NodeID            string     `json:"node_id"`
	NodeType          string     `json:"node_type"`
	NodeRevision      string     `json:"node_revision"`
	ProjectionVersion string     `json:"projection_version"`
	PageOrdinal       uint64     `json:"page_ordinal"`
	Name              string     `json:"name"`
	PageText          string     `json:"page_text"`
	Retired           bool       `json:"retired"`
	SearchGeneration  int64      `json:"search_generation"`
	Access            pageAccess `json:"access"`
}

// retiredDocument replaces an obsolete page. It has no page_text, name,
// access, or generated semantic fields.
type retiredDocument struct {
	NodeID           string `json:"node_id"`
	NodeRevision     string `json:"node_revision"`
	PageOrdinal      uint64 `json:"page_ordinal"`
	Retired          bool   `json:"retired"`
	SearchGeneration int64  `json:"search_generation"`
}

type pageAccess struct {
	Versions   []string `json:"versions"`
	Keys       []string `json:"keys"`
	Generation int64    `json:"generation"`
}

type bulkTarget struct {
	Index       string `json:"_index"`
	ID          string `json:"_id"`
	Version     int64  `json:"version,omitempty"`
	VersionType string `json:"version_type,omitempty"`
}

type bulkIndexHeader struct {
	Index bulkTarget `json:"index"`
}

type bulkUpdateHeader struct {
	Update bulkTarget `json:"update"`
}

type accessScriptBody struct {
	Script accessScript `json:"script"`
}

type accessScript struct {
	Source string             `json:"source"`
	Lang   string             `json:"lang"`
	Params accessScriptParams `json:"params"`
}

type accessScriptParams struct {
	Generation int64      `json:"generation"`
	Access     pageAccess `json:"access"`
}

// accessUpdateScript orders access-only updates by search_generation. It
// writes search_generation and access only when the stored search_generation
// is lower than the update generation and the page is not retired. Otherwise
// it makes no change.
//
// An access-only update uses this script instead of external versioning
// because OpenSearch 3.8.0 rejects an update action with version or
// version_type. The script runs on the primary shard under the document lock.
// Each accepted update raises the internal _version by one and sets a
// strictly higher search_generation. The internal _version stays at or below
// search_generation.
//
// Content writes and retirements are index actions with version_type
// external_gte at the FoundationDB generation. A retirement at the current
// generation passes the version check. A delayed content write below a
// retirement or content write generation fails the version check. A delayed
// content write below an access update generation can pass when the internal
// _version is lower. The stale claim cannot checkpoint because the access
// change rewrote that content work to the access generation in FoundationDB.
// The next claim rewrites the page at the current generation.
//
// The update omits page_text. skip_existing_embedding leaves the stored
// chunks and sparse weights unchanged, and ML Commons runs no inference.
//
//go:embed access_update.painless
var accessUpdateScript string

func encodePageDocument(ctx context.Context, intent searchdomain.WriteIntent) ([]byte, error) {
	header := bulkIndexHeader{Index: externalTarget(intent.Work.Target, intent.DocumentID, intent.Work.Generation)}
	document := pageDocument{
		NodeID: intent.Work.NodeID.String(), NodeType: intent.Page.NodeType,
		NodeRevision: intent.Page.Revision, ProjectionVersion: intent.Page.ProjectionVersion,
		PageOrdinal: intent.Page.Ordinal, Name: intent.Page.Name, PageText: intent.Page.Text,
		Retired: false, SearchGeneration: intent.Work.Generation, Access: toPageAccess(intent.Page.Access),
	}
	return encodeBulkLines(ctx, header, document)
}

func encodeRetirement(ctx context.Context, work searchdomain.Work, document searchdomain.IssuedDocument) ([]byte, error) {
	header := bulkIndexHeader{Index: externalTarget(work.Target, document.DocumentID, work.Generation)}
	retired := retiredDocument{
		NodeID: work.NodeID.String(), NodeRevision: strconv.FormatInt(document.Revision, 10),
		PageOrdinal: document.Ordinal, Retired: true, SearchGeneration: work.Generation,
	}
	return encodeBulkLines(ctx, header, retired)
}

func encodeAccessUpdate(ctx context.Context, work searchdomain.Work, documentID string, access node.SearchAccess) ([]byte, error) {
	header := bulkUpdateHeader{Update: bulkTarget{Index: work.Target, ID: documentID, Version: 0, VersionType: ""}}
	body := accessScriptBody{Script: accessScript{
		Source: accessUpdateScript, Lang: "painless",
		Params: accessScriptParams{Generation: work.Generation, Access: toPageAccess(access)},
	}}
	return encodeBulkLines(ctx, header, body)
}

func externalTarget(index, documentID string, generation int64) bulkTarget {
	return bulkTarget{Index: index, ID: documentID, Version: generation, VersionType: "external_gte"}
}

func toPageAccess(access node.SearchAccess) pageAccess {
	return pageAccess{Versions: access.Versions, Keys: access.Keys, Generation: access.Generation}
}

type bulkLine interface {
	bulkIndexHeader | bulkUpdateHeader
}

type bulkBody interface {
	pageDocument | retiredDocument | accessScriptBody
}

func encodeBulkLines[Header bulkLine, Body bulkBody](ctx context.Context, header Header, body Body) ([]byte, error) {
	first, err := json.Marshal(header)
	if err != nil {
		wrapped := fmt.Errorf("encode bulk action: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.bulk.encode_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	second, err := json.Marshal(body)
	if err != nil {
		wrapped := fmt.Errorf("encode bulk document: %w", err)
		telemetry.L(ctx).ErrorContext(ctx, "search.bulk.encode_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	encoded := make([]byte, 0, len(first)+len(second)+2)
	encoded = append(encoded, first...)
	encoded = append(encoded, '\n')
	encoded = append(encoded, second...)
	return append(encoded, '\n'), nil
}
