package tools

import (
	"encoding/json"
	"sort"

	"goodkind.io/tack/internal/domain/node"
)

func renderPropertyDefs(defs []*node.PropertyDef) string {
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	items := make([]markdownItem, 0, len(defs))
	for _, def := range defs {
		fields := []markdownField{
			markdownCodeFieldValue("Type", string(def.Type)),
			markdownFieldValue("Indexed", yesNo(def.Indexed)),
			markdownFieldValue("Required", yesNo(def.Required)),
		}
		if def.ReferenceTargetTypeKey != "" {
			fields = append(fields, markdownCodeFieldValue("Target type", def.ReferenceTargetTypeKey))
		}
		if len(def.AppliesToFeatures) > 0 {
			fields = append(fields, markdownFieldValue("Applies to features", codeList(def.AppliesToFeatures)))
		}
		if len(def.Options) > 0 {
			fields = append(fields, markdownFieldValue("Values", enumOptions(def.Options)))
		}
		items = append(items, markdownItem{Title: markdownCodeValue(def.Name), Fields: fields})
	}
	data := collectionTemplateData{Heading: "Property definitions", Count: len(defs), Noun: "property definitions", Items: items}
	return executeMarkdownTemplate("collection.md.tmpl", data)
}

func renderProperties(rc *renderCtx, view *node.NodeView) string {
	ident := identifierFor(view, rc)
	if ident == "" {
		ident = view.ID.String()
	}
	body, err := json.MarshalIndent(view.Props, "", "  ")
	if err != nil {
		body = []byte("{}")
	}
	data := rawJSONTemplateData{
		Heading: "Raw properties for `" + ident + "`",
		Intro:   "This is the raw Props escape hatch. Prefer ergonomic identifiers from list and get responses for normal follow-up tool calls.",
		Body:    string(body),
	}
	return executeMarkdownTemplate("raw_json.md.tmpl", data)
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func enumOptions(options []node.EnumOption) string {
	values := make([]string, 0, len(options))
	for _, option := range options {
		values = append(values, option.Key)
	}
	return codeList(values)
}
