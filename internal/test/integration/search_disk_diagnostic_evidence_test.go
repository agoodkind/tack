package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/testenv"
)

type diskProbeFilesystem struct {
	Nodes map[string]struct {
		FS struct {
			Total map[string]json.RawMessage `json:"total"`
		} `json:"fs"`
	} `json:"nodes,omitempty"`
}

// The proxy injects missing fields or zero counters into real engine
// responses. It does not change the engine's data or resource settings.
func TestSearchDiskDiagnosticsRequireCounters(t *testing.T) {
	engine := testenv.OpenSearch(t)

	for _, fault := range []string{
		"missing_nodes",
		"missing_available_with_reported_block",
		"zero_free",
	} {
		t.Run(
			fault,
			func(t *testing.T) {
				proxy, endpoint, caPath := startPredictHoldProxy(t, engine.Endpoint, engine.CA, "")
				proxy.engine.ModifyResponse = func(response *http.Response) error {
					return injectDiskDiagnosticFault(response, fault)
				}
				certificate, err := os.ReadFile(caPath)
				if err != nil {
					t.Fatalf("read the proxy certificate: %v", err)
				}

				proxied := engine
				proxied.Endpoint, proxied.CA = endpoint, string(certificate)
				err = testenv.CheckOpenSearchDiskReadiness(t.Context(), proxied)
				t.Logf("injected diagnostic fault %s returned %v", fault, err)
				if fault == "zero_free" {
					if err != nil {
						t.Fatalf("zero free bytes did not count as a present measurement: %v", err)
					}

					return
				}

				problem := "filesystem response did not include any nodes"
				if fault == "missing_available_with_reported_block" {
					problem = "available_in_bytes"
					if err == nil ||
						!strings.Contains(err.Error(), `cluster.blocks.create_index="true"`) {
						t.Fatalf("the missing measurement lost the injected block context: %v", err)
					}
				}

				if err == nil || !strings.Contains(err.Error(), problem) {
					t.Fatalf(
						"the injected missing filesystem evidence returned %v, want %q",
						err,
						problem,
					)
				}
			},
		)
	}
}

func injectDiskDiagnosticFault(response *http.Response, fault string) error {
	filesystem := strings.HasSuffix(response.Request.URL.Path, "/stats/fs")
	settings := response.Request.URL.Path == "/_cluster/settings" &&
		fault == "missing_available_with_reported_block"
	if !filesystem && !settings {
		return nil
	}

	payload, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err != nil {
		return fmt.Errorf("read the diagnostic response for fault injection: %w", err)
	}

	if closeErr != nil {
		return fmt.Errorf("close the diagnostic response for fault injection: %w", closeErr)
	}

	if settings {
		var values opensearchapi.ClusterGetSettingsResp
		if err := json.Unmarshal(payload, &values); err != nil {
			return fmt.Errorf("decode the settings response for fault injection: %w", err)
		}

		values.Transient = map[string]json.RawMessage{
			"cluster.blocks.create_index": json.RawMessage(`"true"`),
		}
		payload, err = json.Marshal(values)
	} else {
		var values diskProbeFilesystem
		if err := json.Unmarshal(payload, &values); err != nil {
			return fmt.Errorf("decode the filesystem response for fault injection: %w", err)
		}

		if fault == "missing_nodes" {
			values.Nodes = nil
		}

		for _, node := range values.Nodes {
			if fault == "zero_free" {
				node.FS.Total["free_in_bytes"], node.FS.Total["available_in_bytes"] = json.RawMessage(
					"0",
				), json.RawMessage("0")
			} else {
				delete(node.FS.Total, "available_in_bytes")
			}
		}

		payload, err = json.Marshal(values)
	}

	if err != nil {
		return fmt.Errorf("encode the injected diagnostic response: %w", err)
	}

	response.Body = io.NopCloser(bytes.NewReader(payload))
	response.ContentLength = int64(len(payload))
	response.Header.Set("Content-Length", strconv.Itoa(len(payload)))

	return nil
}
