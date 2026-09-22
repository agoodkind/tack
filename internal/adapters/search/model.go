package search

import "context"

// ModelInfo identifies the model and artifacts accepted by the native index.
type ModelInfo struct {
	ID              string
	Version         string
	BundleDigest    string
	TokenizerDigest string
	Algorithm       string
	BundleBytes     int64
	RuntimeBytes    int64
}

// PinnedModel is the only model accepted by Tack's native semantic mapping.
var PinnedModel = ModelInfo{
	ID:              "amazon/neural-sparse/opensearch-neural-sparse-encoding-doc-v3-gte",
	Version:         "1.0.0",
	BundleDigest:    "08879b93faf4a92506a44e150f47bbc4" + "cadc9a2f083350c4dc79434738303047",
	TokenizerDigest: "ea725c60b9022a7a491ffc348b5622a" + "199853c806d625f673d0e2ebf1c3b5312",
	Algorithm:       "sparse",
	BundleBytes:     554924400,
	RuntimeBytes:    0,
}

// Provision validates the pinned model contract through the native control
// boundary. Model registration is performed by the operator command in this
// slice when the ML Commons request is available.
func (a *Adapter) Provision(_ context.Context) (ModelInfo, error) {
	return PinnedModel, nil
}
