package tools

import (
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	truncatedTitleSuffix = "…`"
	truncatedValueSuffix = "…"
	// shortenedFieldFloorBytes sets the minimum number of bytes to keep before
	// shortenMarkdownValue adjusts to a character boundary and adds its suffix.
	shortenedFieldFloorBytes = 16
)

// fitItemToBudget preserves fields with Whole set and shortens other field
// values from longest to shortest when an item exceeds budget. It shortens
// the title only if the item still exceeds budget.
func fitItemToBudget(item markdownItem, budget int) markdownItem {
	if markdownItemBytes(item) <= budget {
		return item
	}
	fields := slices.Clone(item.Fields)
	order := make([]int, len(fields))
	for index := range order {
		order[index] = index
	}
	slices.SortStableFunc(order, func(left, right int) int {
		return len(fields[right].Value) - len(fields[left].Value)
	})
	item.Fields = fields
	for _, index := range order {
		excess := markdownItemBytes(item) - budget
		if excess <= 0 {
			return item
		}
		if fields[index].Whole {
			continue
		}
		fields[index].Value = shortenMarkdownValue(fields[index].Value, excess, shortenedFieldFloorBytes)
	}
	if excess := markdownItemBytes(item) - budget; excess > 0 {
		item.Title = shortenMarkdownValue(item.Title, excess, 1)
	}
	return item
}

// shortenMarkdownValue cuts a value at a UTF-8 character boundary and adds
// an ellipsis. It preserves the closing backtick of a Markdown code value.
func shortenMarkdownValue(value string, excess, floor int) string {
	suffix := truncatedValueSuffix
	if len(value) > 1 && strings.HasPrefix(value, "`") && strings.HasSuffix(value, "`") {
		suffix = truncatedTitleSuffix
	}
	keep := max(len(value)-excess-len(suffix), floor)
	if keep+len(suffix) >= len(value) {
		return value
	}
	for keep > 1 && !utf8.RuneStart(value[keep]) {
		keep--
	}
	return value[:keep] + suffix
}
