package integration_test

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/testenv"
)

func readFixturePassword(t *testing.T, directory, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(contents)) == "" {
		t.Fatal("the synthetic credential file is empty")
	}
	return strings.TrimSpace(string(contents))
}

func secretSearchRequest(t *testing.T, engine testenv.OpenSearchFixture, username, password, method, path string, body map[string]string) int {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if body == nil {
		encoded = nil
	}
	response := secretSearchResponse(t, engine, username, password, method, path, encoded)
	defer response.Body.Close()
	return response.StatusCode
}

func secretSearchResponse(t *testing.T, engine testenv.OpenSearchFixture, username, password, method, path string, encoded []byte) *http.Response {
	t.Helper()
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(engine.CA)) {
		t.Fatal("the fixture CA could not be parsed")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: time.Minute}
	request, err := http.NewRequestWithContext(t.Context(), method, engine.Endpoint+path, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth(username, password)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func verifySecretSearchConnectorDenial(t *testing.T, engine testenv.OpenSearchFixture, password string) {
	t.Helper()
	encoded, err := json.Marshal(struct {
		Query struct {
			MatchAll struct{} `json:"match_all"`
		} `json:"query"`
		Source         []string `json:"_source"`
		Size           int      `json:"size"`
		TrackTotalHits bool     `json:"track_total_hits"`
	}{Source: []string{"name", "protocol"}, Size: 100, TrackTotalHits: true})
	if err != nil {
		t.Fatal(err)
	}
	response := secretSearchResponse(t, engine, "tack_runtime", password, http.MethodPost, "/_plugins/_ml/connectors/_search", encoded)
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("runtime connector search returned %d, expected %d", response.StatusCode, http.StatusForbidden)
	}
}

func logSecretSearchTaskFailures(t *testing.T, engine testenv.OpenSearchFixture) {
	t.Helper()
	encoded, err := json.Marshal(struct {
		Size   int      `json:"size"`
		Source []string `json:"_source"`
	}{Size: 5, Source: []string{"state", "error"}})
	if err != nil {
		t.Fatal(err)
	}
	response := secretSearchResponse(t, engine, engine.Username, engine.Password, http.MethodPost, "/_plugins/_ml/tasks/_search", encoded)
	defer response.Body.Close()
	var diagnostics struct {
		Hits struct {
			Hits []struct {
				Source struct {
					State string `json:"state"`
					Error string `json:"error"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(response.Body).Decode(&diagnostics); err != nil {
		t.Logf("ML task diagnostics returned status %d and could not be decoded", response.StatusCode)
		return
	}
	for _, hit := range diagnostics.Hits.Hits {
		t.Logf("ML task state=%s error=%s", hit.Source.State, hit.Source.Error)
	}
}
