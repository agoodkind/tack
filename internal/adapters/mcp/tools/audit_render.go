package tools

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
)

func renderAuditRows(rows []audit.Row) string {
	items := make([]markdownItem, 0, len(rows))
	for _, row := range rows {
		fields := []markdownField{
			markdownFieldValue("Time", formatDisplayTime(row.EventTime)),
			markdownCodeFieldValue("Actor id", row.ActorID.String()),
			markdownCodeFieldValue("Action", row.Action),
			markdownCodeFieldValue("Outcome", string(row.Outcome)),
			markdownFieldValue("Entity", fmt.Sprintf("`%s` `%s`", row.EntityKind, row.EntityID)),
			markdownFieldValue("Request", auditOptionalValue(row.Context.RequestID)),
			markdownFieldValue("Trace", auditOptionalValue(row.Context.TraceID)),
			markdownFieldValue("Sequence", fmt.Sprintf("%d", row.Seq)),
		}
		items = append(items, markdownItem{Title: markdownCodeValue(row.EventID.String()), Fields: fields})
	}
	data := collectionTemplateData{Heading: "Audit events", Count: len(rows), Noun: "audit events", Items: items}
	return executeMarkdownTemplate("collection.md.tmpl", data)
}

func renderAuditRow(row audit.Row) string {
	fields := []markdownField{
		markdownCodeFieldValue("Action", row.Action),
		markdownCodeFieldValue("Outcome", string(row.Outcome)),
		markdownFieldValue("Time", formatDisplayTime(row.EventTime)),
		markdownCodeFieldValue("Actor id", row.ActorID.String()),
		markdownFieldValue("Actor kind", fmt.Sprintf("%d", row.ActorKind)),
		markdownFieldValue("Entity", fmt.Sprintf("`%s` `%s`", row.EntityKind, row.EntityID)),
		markdownFieldValue("Sequence", fmt.Sprintf("%d on shard %d", row.Seq, row.Shard)),
	}
	if row.IdempotencyKey != "" {
		fields = append(fields, markdownCodeFieldValue("Idempotency key", row.IdempotencyKey))
	}
	data := detailTemplateData{Heading: fmt.Sprintf("Audit event `%s`", row.EventID), Fields: fields, Sections: auditSections(row)}
	return executeMarkdownTemplate("detail.md.tmpl", data)
}

func renderAuditRedaction(actorID uuid.UUID, rows int64) string {
	fields := []markdownField{
		markdownCodeFieldValue("Actor id", actorID.String()),
		markdownFieldValue("Rows redacted", fmt.Sprintf("%d", rows)),
		markdownFieldValue("Scope", "audit PII payloads only"),
	}
	data := nodeTemplateData{Heading: "Redacted audit actor data", Fields: fields}
	return executeMarkdownTemplate("node.md.tmpl", data)
}

func auditOptionalValue(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func auditSections(row audit.Row) []markdownSection {
	sections := make([]markdownSection, 0, 4)
	sections = appendJSONSection(sections, "Context", row.Context)
	if row.Delta != nil {
		sections = appendJSONSection(sections, "Delta", row.Delta)
	}
	if row.Error != nil {
		sections = appendJSONSection(sections, "Error", row.Error)
	}
	if len(row.Extra) != 0 && string(row.Extra) != "null" {
		sections = appendJSONSection(sections, "Extra", row.Extra)
	}
	return sections
}

// auditSectionValue is the set of audit payloads a detail section renders:
// the decoded context, delta, and error, plus the raw verb-specific extra.
type auditSectionValue interface {
	audit.EventContext | *audit.Delta | *audit.EventError | json.RawMessage
}

func appendJSONSection[V auditSectionValue](sections []markdownSection, heading string, value V) []markdownSection {
	formatted, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return sections
	}
	return append(sections, markdownSection{Heading: heading, Body: string(formatted)})
}
