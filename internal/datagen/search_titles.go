package datagen

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
)

const (
	searchItemPrefix   = "- "
	searchFieldPrefix  = "  - "
	searchTypeField    = "  - Type: "
	searchTitleQuote   = "`"
	printedReference   = "- Reference: `"
	printedType        = "- Type: `"
	searchKeySeparator = "\x00"
	// searchShortenedSuffix ends a value that search shortened to fit the
	// response byte budget.
	searchShortenedSuffix = "…"
	searchTitleShared     = "search returned the result %q of type %q, which names more than one registered node"
	searchTitleUnmatched  = "search returned the result %q of type %q, which matches no recorded node"
)

// errSearchResultUnresolved reports a result that the resolver cannot match
// to a node.
var errSearchResultUnresolved = errors.New("search result identifies no node")

// SearchItem records the printed title and node type of one search result.
// Search uses the node's declared reference as its title, or its name when
// the node type does not declare a reference.
type SearchItem struct {
	Title    string
	NodeType string
}

// SearchResolver resolves a result that the title registry did not match.
// It returns a real node ID or an error when it cannot resolve the result.
type SearchResolver func(context.Context, SearchItem) (uuid.UUID, error)

// SearchIdentities pairs a title and node type registry with an optional
// resolver for unmatched search results.
type SearchIdentities struct {
	Titles  *SearchTitles
	Resolve SearchResolver
}

// IDs returns real node IDs in response order and records each resolved result
// in Titles. It fails when a title and node type match multiple recorded nodes
// or when an unmatched result lacks a resolver or the resolver returns an error.
func (i SearchIdentities) IDs(ctx context.Context, items []SearchItem) ([]uuid.UUID, error) {
	nodeIDs := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		nodeID, found, err := i.Titles.lookup(item)
		if err != nil {
			return nil, err
		}
		if !found {
			if i.Resolve == nil {
				return nil, fmt.Errorf(searchTitleUnmatched, item.Title, item.NodeType)
			}
			nodeID, err = i.Resolve(ctx, item)
			if err != nil {
				return nil, loggedError(ctx, fmt.Sprintf("qa datagen: resolve search result %q of type %q", item.Title, item.NodeType), err)
			}
			i.Titles.Add(item, nodeID)
		}
		nodeIDs = append(nodeIDs, nodeID)
	}
	return nodeIDs, nil
}

// SearchTitles maps recorded title and node type pairs to node IDs.
type SearchTitles struct {
	mutex  sync.Mutex
	ids    map[string]uuid.UUID
	shared map[string]bool
}

// NewSearchTitles creates an empty title and node type registry.
func NewSearchTitles() *SearchTitles {
	return &SearchTitles{mutex: sync.Mutex{}, ids: map[string]uuid.UUID{}, shared: map[string]bool{}}
}

// Add records the node ID for item's title and node type. It marks the pair
// as shared when another node ID already uses it.
func (s *SearchTitles) Add(item SearchItem, nodeID uuid.UUID) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	key := searchItemKey(item)
	if existing, found := s.ids[key]; found && existing != nodeID {
		s.shared[key] = true
	}
	s.ids[key] = nodeID
}

// lookup matches item's title and node type to a recorded node ID. It tries
// shortenedID when the exact pair does not match and returns an error when
// the exact pair matches multiple nodes.
func (s *SearchTitles) lookup(item SearchItem) (uuid.UUID, bool, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	key := searchItemKey(item)
	if s.shared[key] {
		return uuid.Nil, false, fmt.Errorf(searchTitleShared, item.Title, item.NodeType)
	}
	if nodeID, found := s.ids[key]; found {
		return nodeID, true, nil
	}
	nodeID, found := s.shortenedID(item)
	return nodeID, found, nil
}

// SearchResultItems uses SearchResultBlocks to read each result's title
// and node type in response order.
func SearchResultItems(text string) []SearchItem {
	blocks := SearchResultBlocks(text)
	items := make([]SearchItem, 0, len(blocks))
	for _, block := range blocks {
		lines := strings.Split(block, "\n")
		item := SearchItem{Title: unquoteSearchValue(strings.TrimPrefix(lines[0], searchItemPrefix)), NodeType: ""}
		for _, line := range lines[1:] {
			if nodeType, found := strings.CutPrefix(line, searchTypeField); found {
				item.NodeType = unquoteSearchValue(nodeType)
			}
		}
		items = append(items, item)
	}
	return items
}

// SearchResultBlocks splits a tack_search page into result blocks in response
// order. Each block contains a title line followed by its field lines.
func SearchResultBlocks(text string) []string {
	blocks := make([]string, 0)
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, searchItemPrefix):
			blocks = append(blocks, line)
		case strings.HasPrefix(line, searchFieldPrefix) && len(blocks) > 0:
			blocks[len(blocks)-1] += "\n" + line
		}
	}
	return blocks
}

// SearchItem reads the printed reference and node type from a write
// confirmation.
func (r Result) SearchItem() SearchItem {
	return SearchItem{Title: r.codeField(printedReference), NodeType: r.codeField(printedType)}
}

// codeField returns the code value after marker in the result text.
func (r Result) codeField(marker string) string {
	_, value, found := strings.Cut(r.Text(), marker)
	if !found {
		return ""
	}
	field, _, closed := strings.Cut(value, searchTitleQuote)
	if !closed {
		return ""
	}
	return field
}

// shortenedID removes searchShortenedSuffix from a shortened title and
// finds recorded titles of the same node type that start with that prefix.
// It returns an ID only when those titles identify one node. The caller
// must lock mutex.
func (s *SearchTitles) shortenedID(item SearchItem) (uuid.UUID, bool) {
	prefix, shortened := strings.CutSuffix(item.Title, searchShortenedSuffix)
	if !shortened {
		return uuid.Nil, false
	}
	typeSuffix := searchKeySeparator + item.NodeType
	matched := uuid.Nil
	for key, nodeID := range s.ids {
		if !strings.HasSuffix(key, typeSuffix) || !strings.HasPrefix(key, prefix) {
			continue
		}
		if matched != uuid.Nil && matched != nodeID {
			return uuid.Nil, false
		}
		matched = nodeID
	}
	return matched, matched != uuid.Nil
}

func searchItemKey(item SearchItem) string {
	return item.Title + searchKeySeparator + item.NodeType
}

func unquoteSearchValue(value string) string {
	return strings.TrimSuffix(strings.TrimPrefix(value, searchTitleQuote), searchTitleQuote)
}
