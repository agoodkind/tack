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

// accessRetryOnConflict is the retry_on_conflict count of an access update.
// A 409 after these retries is a rejection.
const accessRetryOnConflict = 3

type bulkTarget struct {
	Index           string `json:"_index"`
	ID              string `json:"_id"`
	IfSeqNo         *int64 `json:"if_seq_no,omitempty"`
	IfPrimaryTerm   *int64 `json:"if_primary_term,omitempty"`
	RetryOnConflict int    `json:"retry_on_conflict,omitempty"`
}

type bulkIndexHeader struct {
	Index bulkTarget `json:"index"`
}

type bulkCreateHeader struct {
	Create bulkTarget `json:"create"`
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
	Generation  int64      `json:"generation"`
	Access      pageAccess `json:"access"`
	StaleMarker string     `json:"stale_marker"`
}

// staleGenerationMarker is the message the access update script throws for
// an update below the stored search_generation. The bulk parser maps an
// item error with this message to ErrObsoleteWrite.
const staleGenerationMarker = "tack stale search generation"

// accessUpdateScript orders access-only updates by search_generation. It
// writes search_generation and access when the stored search_generation is
// lower than the update generation and the page is not retired. It throws
// params.stale_marker when the stored search_generation is higher. It makes
// no change at the same generation or on a retired page.
//
// An access-only update uses a script because OpenSearch 3.8.0 rejects an
// update action with version or version_type. The script runs on the primary
// shard under the document lock.
//
// Content writes and retirements read the stored page first (writeGuarded)
// and send index actions with if_seq_no and if_primary_term from that read.
//
// The update omits page_text. skip_existing_embedding leaves the stored
// chunks and sparse weights unchanged, and ML Commons runs no inference.
//
//go:embed access_update.painless
var accessUpdateScript string

func encodePageDocument(ctx context.Context, intent searchdomain.WriteIntent, precondition pagePrecondition) ([]byte, error) {
	document := pageDocument{
		NodeID: intent.Work.NodeID.String(), NodeType: intent.Page.NodeType,
		NodeRevision: intent.Page.Revision, ProjectionVersion: intent.Page.ProjectionVersion,
		PageOrdinal: intent.Page.Ordinal, Name: intent.Page.Name, PageText: intent.Page.Text,
		Retired: false, SearchGeneration: intent.Work.Generation, Access: toPageAccess(intent.Page.Access),
	}
	return encodeGuarded(ctx, intent.Work.Target, intent.DocumentID, precondition, document)
}

func encodeRetirement(ctx context.Context, work searchdomain.Work, document searchdomain.IssuedDocument, precondition pagePrecondition) ([]byte, error) {
	retired := retiredDocument{
		NodeID: work.NodeID.String(), NodeRevision: strconv.FormatInt(document.Revision, 10),
		PageOrdinal: document.Ordinal, Retired: true, SearchGeneration: work.Generation,
	}
	return encodeGuarded(ctx, work.Target, document.DocumentID, precondition, retired)
}

// encodeGuarded encodes a create action for an absent page, or an index
// action that requires the read sequence number and primary term.
func encodeGuarded[Body pageDocument | retiredDocument](ctx context.Context, index, documentID string, precondition pagePrecondition, body Body) ([]byte, error) {
	if precondition.Create {
		target := bulkTarget{Index: index, ID: documentID, IfSeqNo: nil, IfPrimaryTerm: nil, RetryOnConflict: 0}
		return encodeBulkLines(ctx, bulkCreateHeader{Create: target}, body)
	}
	seqNo, primaryTerm := precondition.SeqNo, precondition.PrimaryTerm
	target := bulkTarget{Index: index, ID: documentID, IfSeqNo: &seqNo, IfPrimaryTerm: &primaryTerm, RetryOnConflict: 0}
	return encodeBulkLines(ctx, bulkIndexHeader{Index: target}, body)
}

func encodeAccessUpdate(ctx context.Context, work searchdomain.Work, documentID string, access node.SearchAccess) ([]byte, error) {
	target := bulkTarget{Index: work.Target, ID: documentID, IfSeqNo: nil, IfPrimaryTerm: nil, RetryOnConflict: accessRetryOnConflict}
	body := accessScriptBody{Script: accessScript{
		Source: accessUpdateScript, Lang: "painless",
		Params: accessScriptParams{Generation: work.Generation, Access: toPageAccess(access), StaleMarker: staleGenerationMarker},
	}}
	return encodeBulkLines(ctx, bulkUpdateHeader{Update: target}, body)
}

func toPageAccess(access node.SearchAccess) pageAccess {
	return pageAccess{Versions: access.Versions, Keys: access.Keys, Generation: access.Generation}
}

type bulkLine interface {
	bulkIndexHeader | bulkCreateHeader | bulkUpdateHeader
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
