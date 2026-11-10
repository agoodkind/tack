package search

import "github.com/opensearch-project/opensearch-go/v5/opensearchapi"

func bulkResponseItems(
	response *opensearchapi.BulkResp,
	capacity int,
) []opensearchapi.BulkRespItem {
	items := make([]opensearchapi.BulkRespItem, 0, capacity)

	for _, operation := range response.Items {
		for _, item := range []*opensearchapi.BulkRespItem{
			operation.Create,
			operation.Delete,
			operation.Index,
			operation.Update,
		} {
			if item != nil {
				items = append(items, *item)
			}
		}
	}

	return items
}

func bulkFailureReason(item opensearchapi.BulkRespItem) string {
	reason := ""
	if item.Error != nil {
		reason = item.Error.Type
		if item.Error.Reason != nil {
			reason += ": " + *item.Error.Reason
		}
	}

	return reason
}
