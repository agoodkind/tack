package datagen

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ToolArguments is the fully enumerated argument object sent to tools/call.
type ToolArguments struct {
	WorkspaceReference string         `json:"workspace_reference,omitempty"`
	ProjectReference   string         `json:"project_reference,omitempty"`
	IssueReference     string         `json:"issue_reference,omitempty"`
	Name               string         `json:"name,omitempty"`
	Properties         NodeProperties `json:"properties,omitempty"`
	NodeID             string         `json:"node_id,omitempty"`
	Query              string         `json:"query,omitempty"`
	NodeType           string         `json:"node_type,omitempty"`
	Direction          string         `json:"direction,omitempty"`
	SourceID           string         `json:"source_id,omitempty"`
	RelationType       string         `json:"relation_type,omitempty"`
	TargetID           string         `json:"target_id,omitempty"`
	ActorID            string         `json:"actor_id,omitempty"`
}

// NodeProperties is a typed raw-JSON property object.
type NodeProperties map[string]json.RawMessage

func newProperties() NodeProperties {
	return make(NodeProperties)
}

func (p NodeProperties) setString(name, value string) {
	p[name] = json.RawMessage(strconv.Quote(value))
}

func (p NodeProperties) setInt(name string, value int) {
	p[name] = json.RawMessage(strconv.Itoa(value))
}

func (p NodeProperties) setBool(name string, value bool) {
	p[name] = json.RawMessage(strconv.FormatBool(value))
}

func (p NodeProperties) setNull(name string) {
	p[name] = json.RawMessage("null")
}

func (p NodeProperties) setStrings(name string, values []string) {
	var builder strings.Builder
	builder.WriteByte('[')
	for index, value := range values {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.Quote(value))
	}
	builder.WriteByte(']')
	p[name] = json.RawMessage(builder.String())
}
