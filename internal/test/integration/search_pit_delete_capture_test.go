package integration_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"testing"
)

// pitDeletePath is the OpenSearch request path Tack uses to delete a point in time.
const pitDeletePath = "/_search/point_in_time"

// pitDeleteBody decodes the pit_id list from a point in time delete request body.
type pitDeleteBody struct {
	PITID []string `json:"pit_id"`
}

// recordDelete reads the delete request body, records its point in time IDs under the mutex,
// restores the body, and forwards the request to OpenSearch.
// It answers HTTP 400 if it cannot read or decode the body.
func (capture *searchCapture) recordDelete(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, "read point in time delete body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var decoded pitDeleteBody
	if err := json.Unmarshal(body, &decoded); err != nil {
		http.Error(writer, "decode point in time delete body: "+err.Error(), http.StatusBadRequest)
		return
	}
	capture.mutex.Lock()
	capture.deleted = append(capture.deleted, decoded.PITID...)
	capture.mutex.Unlock()
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	capture.engine.ServeHTTP(writer, request)
}

// requireDeleteSent fails the test unless the proxy forwarded a point in time delete request
// for at least one of ids.
func requireDeleteSent(t *testing.T, capture *searchCapture, ids []string) {
	t.Helper()
	capture.mutex.Lock()
	deleted := slices.Clone(capture.deleted)
	capture.mutex.Unlock()
	if !slices.ContainsFunc(ids, func(id string) bool { return slices.Contains(deleted, id) }) {
		t.Fatalf("proxy did not forward a point in time delete request for any of %v and recorded delete ids %v", ids, deleted)
	}
}
