package foundationdb

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	searchCursorVersion = 1
	// minSearchPageBytes is the smallest accepted page bound. Every nonfinal
	// page ends after the previous page ends because three quarters of a
	// page at this bound exceed the longest UTF-8 sequence.
	minSearchPageBytes = 16
)

// contentCursor binds a continuation to one node, revision, projection
// configuration, pagination version, and byte bound.
type contentCursor struct {
	Version    int    `json:"v"`
	NodeID     string `json:"n"`
	Revision   string `json:"r"`
	Projection string `json:"p"`
	MaxBytes   int    `json:"b"`
	NextOffset int    `json:"o"`
	Ordinal    uint64 `json:"i"`
}

func marshalContentCursor(ctx context.Context, cursor contentCursor) (string, error) {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", searchReadFailure(ctx, "encode content cursor", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func unmarshalContentCursor(ctx context.Context, encoded string) (contentCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return contentCursor{}, searchReadFailure(ctx, "decode content cursor", err)
	}
	var cursor contentCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil {
		return contentCursor{}, searchReadFailure(ctx, "parse content cursor", err)
	}
	if cursor.NextOffset <= 0 || cursor.Ordinal == 0 || cursor.MaxBytes <= 0 || strings.TrimSpace(cursor.Revision) == "" || cursor.Projection == "" {
		return contentCursor{}, errors.New("content cursor fields are invalid")
	}
	return cursor, nil
}

func contentPosition(ctx context.Context, request searchdomain.ContentRequest, revision, projection string) (contentCursor, error) {
	maxBytes := request.MaxBytes
	if maxBytes == 0 {
		maxBytes = searchdomain.MaxPageBytes
	}
	if maxBytes < minSearchPageBytes || maxBytes > searchdomain.MaxPageBytes {
		return contentCursor{}, fmt.Errorf("content page bound must be between %d and %d bytes", minSearchPageBytes, searchdomain.MaxPageBytes)
	}
	if request.Cursor == "" {
		return contentCursor{
			Version: searchCursorVersion, NodeID: request.NodeID.String(),
			Revision: revision, Projection: projection, MaxBytes: maxBytes,
			NextOffset: 0, Ordinal: 0,
		}, nil
	}
	cursor, err := unmarshalContentCursor(ctx, request.Cursor)
	if err != nil {
		return contentCursor{}, err
	}
	if cursor.Version != searchCursorVersion || cursor.NodeID != request.NodeID.String() ||
		cursor.Revision != revision || cursor.Projection != projection || cursor.MaxBytes != maxBytes {
		return contentCursor{}, node.ErrContentChanged
	}
	return cursor, nil
}

// contentPage cuts one page from text at position. A continuation repeats at
// most one quarter of the byte bound as overlap and starts and ends on UTF-8
// boundaries.
func contentPage(ctx context.Context, text string, identity contentIdentity, access node.SearchAccess, position contentCursor) (node.ContentPage, error) {
	if position.NextOffset > len(text) || position.NextOffset == len(text) && position.Ordinal > 0 {
		return node.ContentPage{}, node.ErrContentChanged
	}
	// The check rejects a continuation offset below one overlap because this
	// function writes every continuation cursor at least one overlap past the
	// text start.
	if position.NextOffset < 0 || position.Ordinal > 0 && position.NextOffset < position.MaxBytes/4 {
		return node.ContentPage{}, node.ErrContentChanged
	}
	start := position.NextOffset
	if position.Ordinal > 0 {
		start -= position.MaxBytes / 4
		for start < position.NextOffset && !utf8.RuneStart(text[start]) {
			start++
		}
	}
	end := min(start+position.MaxBytes, len(text))
	for end > position.NextOffset && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	if end <= position.NextOffset {
		return node.ContentPage{}, errors.New("UTF-8 page boundary does not advance")
	}
	page := node.ContentPage{
		NodeID: identity.nodeID, NodeType: identity.nodeType, Revision: identity.revision,
		ProjectionVersion: identity.projection, Name: identity.name, Text: text[start:end],
		Access: access, Ordinal: position.Ordinal, NextCursor: "",
		OverlapBytes: position.NextOffset - start, Done: end == len(text),
	}
	if !page.Done {
		next := position
		next.NextOffset = end
		next.Ordinal++
		cursor, err := marshalContentCursor(ctx, next)
		if err != nil {
			return node.ContentPage{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}
