package datagen

import (
	"strconv"
	"strings"
)

type collectionItem struct {
	Reference string
	Name      string
	NodeType  string
}

func (i collectionItem) referenceIsName() bool {
	return i.Reference != "" && i.Reference == i.Name
}

type collectionField string

const (
	collectionFieldName collectionField = "Name"
	collectionFieldType collectionField = "Type"
)

func (r Result) collectionItems() []collectionItem {
	var items []collectionItem
	current := -1
	inCollection := false
	for _, line := range strings.Split(r.Text(), "\n") {
		if strings.HasSuffix(line, " found.") {
			inCollection = true
			continue
		}
		if !inCollection {
			continue
		}
		if strings.HasPrefix(line, "- ") {
			items = append(items, collectionItem{
				Reference: trimMarkdownValue(strings.TrimPrefix(line, "- ")),
			})
			current = len(items) - 1
			continue
		}
		if current < 0 || !strings.HasPrefix(line, "  - ") {
			continue
		}
		label, value, ok := parseMarkdownField(strings.TrimPrefix(line, "  - "))
		if !ok {
			continue
		}
		switch collectionField(label) {
		case collectionFieldName:
			items[current].Name = value
		case collectionFieldType:
			items[current].NodeType = trimMarkdownValue(value)
		}
	}
	return items
}

func (r Result) field(label string) string {
	prefix := "- " + label + ": "
	for _, line := range strings.Split(r.Text(), "\n") {
		if strings.HasPrefix(line, prefix) {
			return trimMarkdownValue(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func (r Result) intField(label string) int {
	value, err := strconv.Atoi(r.field(label))
	if err != nil {
		return 0
	}
	return value
}

func parseMarkdownField(value string) (string, string, bool) {
	label, fieldValue, ok := strings.Cut(value, ": ")
	if !ok {
		return "", "", false
	}
	return label, fieldValue, true
}

func trimMarkdownValue(value string) string {
	return strings.Trim(strings.TrimSpace(value), "`")
}
