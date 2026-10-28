package datagen

import (
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
	searchTitleShared  = "search returned the result %q of type %q, which names more than one registered node"
)

// searchItemNamespace is the UUID namespace for deriving stand-in result IDs.
var searchItemNamespace = uuid.MustParse("5f2a8d6e-3c41-4b7a-9e10-6d8c2b4f7a93")

// SearchItem records the printed title and node type of one search result.
// Search uses the node's declared reference as its title, or its name when
// the node type does not declare a reference.
type SearchItem struct {
	Title    string
	NodeType string
}

// SearchTitles maps each recorded title and node type to its fixture node ID.
type SearchTitles struct {
	mutex      sync.Mutex
	ids        map[string]uuid.UUID
	shared     map[string]bool
	unresolved map[uuid.UUID]SearchItem
}

// NewSearchTitles returns an empty registry.
func NewSearchTitles() *SearchTitles {
	return &SearchTitles{
		mutex: sync.Mutex{}, ids: map[string]uuid.UUID{}, shared: map[string]bool{},
		unresolved: map[uuid.UUID]SearchItem{},
	}
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

// IDs returns one ID per item in response order. It tries shortenedID when
// an exact title and node type do not match a recorded node. It assigns a
// stable stand-in ID to each remaining item using its title and node type.
// It returns an error when an exact pair identifies multiple recorded nodes.
func (s *SearchTitles) IDs(items []SearchItem) ([]uuid.UUID, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	nodeIDs := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		key := searchItemKey(item)
		if s.shared[key] {
			return nil, fmt.Errorf(searchTitleShared, item.Title, item.NodeType)
		}
		nodeID, found := s.ids[key]
		if !found {
			nodeID, found = s.shortenedID(item)
		}
		if !found {
			nodeID = uuid.NewSHA1(searchItemNamespace, []byte(key))
			s.unresolved[nodeID] = item
		}
		nodeIDs = append(nodeIDs, nodeID)
	}
	return nodeIDs, nil
}

// Unresolved returns the result item for a stand-in ID that IDs created.
func (s *SearchTitles) Unresolved(nodeID uuid.UUID) (SearchItem, bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	item, found := s.unresolved[nodeID]
	return item, found
}

// SearchResultItems returns the title and node type of each result item on
// a tack_search page in response order.
func SearchResultItems(text string) []SearchItem {
	items := make([]SearchItem, 0)
	for _, line := range strings.Split(text, "\n") {
		if title, found := strings.CutPrefix(line, searchItemPrefix); found {
			items = append(items, SearchItem{Title: unquoteSearchValue(title), NodeType: ""})
			continue
		}
		nodeType, found := strings.CutPrefix(line, searchTypeField)
		if found && len(items) > 0 && strings.HasPrefix(line, searchFieldPrefix) {
			items[len(items)-1].NodeType = unquoteSearchValue(nodeType)
		}
	}
	return items
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
