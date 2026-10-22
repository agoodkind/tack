package integration

import (
	"fmt"
	"strings"
	"testing"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"

	"goodkind.io/tack/internal/adapters/search"
)

// TestSearchControlCommandRejectsMappingAndModelMismatches requires the
// audited verify command to fail with an error that includes each mapping or
// model value that differs on a freshly provisioned index. The test changes
// one value at a time.
func TestSearchControlCommandRejectsMappingAndModelMismatches(t *testing.T) {
	control := newSearchControl(t)
	model, err := control.adapter.Provision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tokenizer := search.PinnedModel.TokenizerDigest
	for _, mismatch := range []struct {
		name   string
		mutate func(t *testing.T)
		want   string
	}{
		{
			name: "mapping version",
			mutate: func(t *testing.T) {
				putSearchControlMapping(t, control, metaMapping("unexpected", model.ID, tokenizer))
			},
			want: `mapping version "unexpected" does not match "` + search.MappingVersion + `"`,
		},
		{
			name: "tokenizer",
			mutate: func(t *testing.T) {
				putSearchControlMapping(t, control, metaMapping(search.MappingVersion, model.ID, "unexpected-tokenizer"))
			},
			want: `tokenizer SHA-256 "unexpected-tokenizer" does not match`,
		},
		{
			name: "mapping property",
			mutate: func(t *testing.T) {
				putSearchControlMapping(t, control, `{"properties":{"unexpected_property":{"type":"keyword"}}}`)
			},
			want: "mapping field unexpected_property is not declared",
		},
		{
			name: "registered model",
			mutate: func(t *testing.T) {
				otherModel := registerMismatchModel(t, control.client)
				putSearchControlMapping(t, control, metaMapping(search.MappingVersion, otherModel, tokenizer))
			},
			want: fmt.Sprintf("model name is %q, want %q", mismatchModelName, search.PinnedModel.Name),
		},
	} {
		t.Run(mismatch.name, func(t *testing.T) {
			control.provisionFromEmpty(t)
			mismatch.mutate(t)
			err := control.run("ops", "search", "verify")
			if err == nil || !strings.Contains(err.Error(), mismatch.want) {
				t.Fatalf("%s mismatch error = %v, want %q", mismatch.name, err, mismatch.want)
			}
		})
	}
}

// metaMapping returns a mapping update that replaces only the index _meta.
func metaMapping(mappingVersion, modelID, tokenizer string) string {
	return fmt.Sprintf(`{"_meta":{"mapping_version":%q,"model_id":%q,"tokenizer_sha256":%q}}`, mappingVersion, modelID, tokenizer)
}

func putSearchControlMapping(t *testing.T, control searchControl, body string) {
	t.Helper()
	response, err := control.client.Indices.Mapping.Put(t.Context(), opensearchapi.MappingPutReq{
		Indices: []string{searchControlIndex}, Body: strings.NewReader(body),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Inspect().Response.IsError() {
		t.Fatal(response.Inspect().Response.String())
	}
}
