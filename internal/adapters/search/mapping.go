package search

import "encoding/json"

// IndexSpec contains the immutable settings and mapping identity for one
// physical search index.
type IndexSpec struct {
	Model          ModelInfo
	MappingVersion string
	Primaries      int
	RoutingShards  int
	Replicas       int
}

// IndexInfo contains mapping metadata used by the verification command.
type IndexInfo struct {
	MappingVersion string
	ModelID        string
}

type mappingMeta struct {
	MappingVersion string `json:"mapping_version"`
	ModelID        string `json:"model_id"`
}

type fieldDef struct {
	Type string `json:"type"`
}

type accessDef struct {
	Dynamic string              `json:"dynamic"`
	Fields  map[string]fieldDef `json:"properties"`
}

type semanticChunking struct {
	Algorithm  string             `json:"algorithm"`
	Parameters map[string]float64 `json:"parameters"`
}

type semanticDef struct {
	Type                  string             `json:"type"`
	RawFieldType          string             `json:"raw_field_type"`
	ModelID               string             `json:"model_id"`
	SemanticInfoFieldName string             `json:"semantic_info_field_name"`
	Chunking              []semanticChunking `json:"chunking"`
	SparseEncodingConfig  map[string]float64 `json:"sparse_encoding_config"`
	SkipExistingEmbedding bool               `json:"skip_existing_embedding"`
}

type mappingDef struct {
	Dynamic    string                     `json:"dynamic"`
	Meta       mappingMeta                `json:"_meta"`
	Properties map[string]json.RawMessage `json:"properties"`
}

type indexBody struct {
	Settings indexSettings `json:"settings"`
	Mappings mappingDef    `json:"mappings"`
}

type indexSettings struct {
	NumberOfShards        int `json:"number_of_shards,omitempty"`
	NumberOfRoutingShards int `json:"number_of_routing_shards,omitempty"`
	NumberOfReplicas      int `json:"number_of_replicas,omitempty"`
}

type indexSettingsBody struct {
	Index indexSettings `json:"index"`
}

// Mapping returns the strict native semantic mapping.
func (s IndexSpec) Mapping() mappingDef {
	return mappingDef{
		Dynamic:    "strict",
		Meta:       mappingMeta{MappingVersion: s.MappingVersion, ModelID: s.Model.ID},
		Properties: mappingProperties(s.Model),
	}
}

func mappingProperties(model ModelInfo) map[string]json.RawMessage {
	properties := map[string]json.RawMessage{}
	for key, value := range map[string]string{"page_id": "keyword", "node_id": "keyword", "revision": "long", "node_type": "keyword", "name": "text", "retired": "boolean", "search_generation": "long"} {
		encoded, err := json.Marshal(fieldDef{Type: value})
		if err != nil {
			continue
		}
		properties[key] = encoded
	}
	access, err := json.Marshal(accessDef{Dynamic: "strict", Fields: map[string]fieldDef{"versions": {Type: "keyword"}, "keys": {Type: "keyword"}, "generation": {Type: "long"}}})
	if err != nil {
		return properties
	}
	properties["access"] = access
	semantic, err := json.Marshal(semanticDef{Type: "semantic", RawFieldType: "text", ModelID: model.ID, SemanticInfoFieldName: "page_text_semantic_info", Chunking: []semanticChunking{{Algorithm: "fixed_char_length", Parameters: map[string]float64{"char_limit": 160, "overlap_rate": 0.5, "max_chunk_limit": -1}}}, SparseEncodingConfig: map[string]float64{"prune_ratio": 0.1}, SkipExistingEmbedding: true})
	if err != nil {
		return properties
	}
	properties["page_text"] = semantic
	return properties
}
