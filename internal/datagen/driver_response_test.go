package datagen

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDriverRejectsInvalidRPCEnvelopes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name            string
		body            string
		wantErrorDetail string
	}{
		{name: "wrong version", body: `{"jsonrpc":"1.0","id":"1","result":{}}`},
		{name: "missing version", body: `{"id":"1","result":{}}`},
		{name: "missing id", body: `{"jsonrpc":"2.0","result":{}}`},
		{
			name:            "missing outcome",
			body:            `{"jsonrpc":"2.0","id":"1"}`,
			wantErrorDetail: "must contain exactly one of result or error",
		},
		{
			name: "both outcomes",
			body: `{"jsonrpc":"2.0","id":"1","result":{},` +
				`"error":{"code":-1,"message":"invalid"}}`,
			wantErrorDetail: "must contain exactly one of result or error",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = writer.Write([]byte(testCase.body))
			})
			_, err := NewDriver(testGraph(handler), false, 245).Call(
				t.Context(),
				"token",
				"tack_list_workspaces",
				ToolArguments{},
			)
			if err == nil {
				t.Fatal("Call() error = nil, want invalid envelope error")
			}
			if testCase.wantErrorDetail != "" &&
				!strings.Contains(err.Error(), testCase.wantErrorDetail) {
				t.Fatalf(
					"Call() error = %v, want error containing %q",
					err,
					testCase.wantErrorDetail,
				)
			}
		})
	}
}

func TestDriverListToolsRejectsEmptyName(t *testing.T) {
	t.Parallel()
	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(
			`{"jsonrpc":"2.0","id":"1","result":{"tools":[{"name":""}]}}`,
		))
	})
	_, err := NewDriver(testGraph(handler), false, 245).ListTools(t.Context(), "token")
	if err == nil || !strings.Contains(err.Error(), "empty tool name") {
		t.Fatalf("ListTools() error = %v, want empty-name error", err)
	}
}

func TestReadLastSSEDataAcceptsLargeLine(t *testing.T) {
	t.Parallel()
	want := strings.Repeat("x", 128*1024)
	payload, err := readLastSSEData([]byte("data: " + want + "\n\n"))
	if err != nil {
		t.Fatalf("readLastSSEData() error = %v", err)
	}
	if string(payload) != want {
		t.Fatalf("payload length = %d, want %d", len(payload), len(want))
	}
}

func TestReadLastSSEDataJoinsLastCompleteEvent(t *testing.T) {
	t.Parallel()
	body := []byte(
		"data: ignored\n\n" +
			"event: message\n" +
			"data: {\"jsonrpc\":\n" +
			"data: \"2.0\",\"id\":\"1\",\"result\":{}}\n\n" +
			"data: incomplete",
	)
	payload, err := readLastSSEData(body)
	if err != nil {
		t.Fatalf("readLastSSEData() error = %v", err)
	}
	want := "{\"jsonrpc\":\n\"2.0\",\"id\":\"1\",\"result\":{}}"
	if string(payload) != want {
		t.Fatalf("payload = %q, want %q", payload, want)
	}
}

func TestResponsePayloadRejectsOversizedBody(t *testing.T) {
	t.Parallel()
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(
			strings.NewReader(strings.Repeat("x", maxResponseBodyBytes+1)),
		),
	}
	_, err := responsePayload(t.Context(), "test", response)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("responsePayload() error = %v, want size-limit error", err)
	}
}
