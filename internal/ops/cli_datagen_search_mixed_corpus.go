package ops

import "goodkind.io/tack/internal/datagen"

// searchMixedCorpusThreshold states the pass threshold of the corpus index measurement. The
// search acceptance criteria permit a threshold only from a documented product target.
const searchMixedCorpusThreshold = "none: no documented product target defines a freshness threshold for the corpus index time"

type datagenSearchMixedCorpusIndex struct {
	Threshold string                         `json:"threshold"`
	Waits     []datagenSearchMixedCorpusWait `json:"waits"`
}

type datagenSearchMixedCorpusWait struct {
	Group     string  `json:"group"`
	Tenant    int     `json:"tenant"`
	Nodes     int     `json:"nodes"`
	Checks    int     `json:"checks"`
	ElapsedMs float64 `json:"elapsed_ms"`
}

func searchMixedCorpusIndexResult(waits []datagen.SearchMixedCorpusWait) datagenSearchMixedCorpusIndex {
	results := make([]datagenSearchMixedCorpusWait, 0, len(waits))
	for _, wait := range waits {
		results = append(results, datagenSearchMixedCorpusWait{
			Group: wait.Group, Tenant: wait.Tenant, Nodes: wait.Nodes, Checks: wait.Checks,
			ElapsedMs: searchLoadMilliseconds(wait.Elapsed),
		})
	}
	return datagenSearchMixedCorpusIndex{Threshold: searchMixedCorpusThreshold, Waits: results}
}
