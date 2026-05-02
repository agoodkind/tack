package tools

import (
	"bytes"
	_ "embed"
	"fmt"
	"strings"
	"text/template"

	"goodkind.io/tack/internal/domain/node"
)

//go:embed getting_started.templ
var gettingStartedTemplateSource string

var gettingStartedTemplate = template.Must(template.New("getting_started").Parse(gettingStartedTemplateSource))

type gettingStartedTemplateData struct {
	EntrySlug        string
	EntryParam       string
	NodeTypes        []nodeTypeTemplateRow
	ReferenceSetters []referenceSetterTemplateRow
}

type nodeTypeTemplateRow struct {
	Slug          string
	PluralSlug    string
	FeaturesText  string
	ReferenceText string
}

type referenceSetterTemplateRow struct {
	Tool     string
	Property string
	Target   string
}

func buildGettingStartedText(resolver *Resolver, nodeTypes []*node.NodeType, propertyDefs []*node.PropertyDef) string {
	data := gettingStartedTemplateData{
		EntrySlug:        resolver.entryPointSlug,
		EntryParam:       resolver.EntryPointParamName(),
		NodeTypes:        nodeTypeRows(nodeTypes),
		ReferenceSetters: referenceSetterRows(resolver, nodeTypes, propertyDefs),
	}
	var buf bytes.Buffer
	if err := gettingStartedTemplate.Execute(&buf, data); err != nil {
		panic(fmt.Sprintf("render getting-started resource: %v", err))
	}
	return buf.String()
}

func nodeTypeRows(nodeTypes []*node.NodeType) []nodeTypeTemplateRow {
	rows := make([]nodeTypeTemplateRow, 0, len(nodeTypes))
	for _, nodeType := range nodeTypes {
		if nodeType.Features.Has(node.FeatureExcludeFromGenericTools) {
			continue
		}
		pluralSlug := nodeType.PluralSlug
		if pluralSlug == "" {
			pluralSlug = nodeType.Slug + "s"
		}
		rows = append(rows, nodeTypeTemplateRow{
			Slug:          nodeType.Slug,
			PluralSlug:    pluralSlug,
			FeaturesText:  strings.Join(nodeType.Features, ", "),
			ReferenceText: referenceSummary(nodeType.Reference),
		})
	}
	return rows
}

func referenceSetterRows(
	resolver *Resolver,
	nodeTypes []*node.NodeType,
	propertyDefs []*node.PropertyDef,
) []referenceSetterTemplateRow {
	var rows []referenceSetterTemplateRow
	for _, nodeType := range nodeTypes {
		if nodeType.Features.Has(node.FeatureExcludeFromGenericTools) {
			continue
		}
		for _, def := range propertyDefs {
			if !referencePropertyAppliesToTool(def, nodeType, resolver.typeIndex) {
				continue
			}
			alias := referencePropertyAlias(def.Name)
			rows = append(rows, referenceSetterTemplateRow{
				Tool:     fmt.Sprintf("tack_set_%s_%s", strings.ToLower(nodeType.Slug), alias),
				Property: def.Name,
				Target:   def.ReferenceTargetTypeKey,
			})
		}
	}
	return rows
}
