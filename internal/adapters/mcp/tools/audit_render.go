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
			markdownFieldValue("Entity", fmt.Sprintf("`%s` `%s`", row.EntityKind, row.EntityID)),
			markdownFieldValue("Request", auditContextValue(row.Context, "request_id")),
			markdownFieldValue("Trace", auditContextValue(row.Context, "trace_id")),
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

func auditContextValue(raw []byte, key string) string {
	var context map[string]string
	if err := json.Unmarshal(raw, &context); err != nil {
		return "-"
	}
	value := context[key]
	if value == "" {
		return "-"
	}
	return value
}

func auditSections(row audit.Row) []markdownSection {
	sections := make([]markdownSection, 0, 2)
	sections = appendRawJSONSection(sections, "Context", row.Context)
	sections = appendRawJSONSection(sections, "Delta", row.Delta)
	return sections
}

func appendRawJSONSection(sections []markdownSection, heading string, raw json.RawMessage) []markdownSection {
	if len(raw) == 0 || string(raw) == "null" {
		return sections
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return sections
	}
	formatted, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return sections
	}
	return append(sections, markdownSection{Heading: heading, Body: string(formatted)})
}
