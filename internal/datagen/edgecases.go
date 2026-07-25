package datagen

import (
	"fmt"
	"strings"
	"time"
)

const maxEdgeCaseNameLength = 255

// InjectEdgeCases deterministically adds boundary values to generated properties.
func InjectEdgeCases(
	index int,
	name string,
	properties NodeProperties,
	now time.Time,
) string {
	switch index % 5 {
	case 0:
		properties.setString("qa_text", "")
		properties.setInt("qa_number", 0)
	case 1:
		name = "Ρaylοad verification " + name
		properties.setString("qa_url", "https://example.invalid/παράδειγμα")
	case 2:
		prefix := fmt.Sprintf("Maximum length %03d ", index)
		name = prefix + strings.Repeat("x", maxEdgeCaseNameLength-len(prefix))
	case 3:
		properties.setNull("qa_url")
		properties.setStrings("qa_multi_select", []string{})
	case 4:
		properties.setString(
			"qa_timestamp",
			now.Add(365*24*time.Hour).UTC().Format(time.RFC3339),
		)
		properties.setString(
			"qa_date",
			now.Add(-365*24*time.Hour).UTC().Format(time.DateOnly),
		)
	}
	return name
}

func issueProperties(
	content *Content,
	actor Actor,
	index int,
	now time.Time,
) NodeProperties {
	start := content.Timestamp(now.Add(-730*24*time.Hour), now)
	due := content.Timestamp(now, now.Add(730*24*time.Hour))
	options := []string{"planned", "active", "verified"}
	properties := newProperties()
	properties.setString("description", content.Paragraph())
	properties.setString("priority", []string{"urgent", "high", "medium", "low"}[index%4])
	properties.setString("start_date", start.Format(time.RFC3339))
	properties.setString("due_date", due.Format(time.RFC3339))
	properties.setBool("is_draft", index%7 == 0)
	properties.setString("qa_text", content.Comment())
	properties.setInt("qa_number", index)
	properties.setString("qa_date", start.Format(time.DateOnly))
	properties.setString("qa_select", options[index%len(options)])
	properties.setStrings(
		"qa_multi_select",
		[]string{options[index%len(options)], options[(index+1)%len(options)]},
	)
	properties.setString("qa_url", fmt.Sprintf("https://example.invalid/issues/%d", index+1))
	properties.setBool("qa_checkbox", index%2 == 0)
	properties.setString("qa_timestamp", due.Format(time.RFC3339))
	properties.setString("qa_uuid", actor.UserID.String())
	return properties
}
