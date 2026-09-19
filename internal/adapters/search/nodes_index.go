package search

// nodesCollection is the Meilisearch index that holds every node document.
const nodesCollection = "nodes"

// nodesFilterableAttributes are the equality filters every nodes query uses.
var nodesFilterableAttributes = []string{"org_id", "node_type"}

// nodesSearchableAttributes lists the fields a query matches, in ranking
// order, so a name match ranks above a property match.
var nodesSearchableAttributes = []string{"name", "props"}

// EnsureNodesIndex creates the nodes index when it is missing and applies its
// filterable and searchable attributes. The server, the repair console, and
// search-reindex call it before they index, so an index created on an empty
// Meilisearch can still be filtered by org.
func EnsureNodesIndex(client *Client) error {
	return client.EnsureIndex(nodesCollection, nodesFilterableAttributes, nodesSearchableAttributes)
}
