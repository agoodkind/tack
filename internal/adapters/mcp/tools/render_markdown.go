package tools

import (
	"fmt"
	"strings"
	"time"
)

var markdownDisplayLocation = loadMarkdownDisplayLocation()

func loadMarkdownDisplayLocation() *time.Location {
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		return time.Local
	}
	return location
}

func formatDisplayTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(markdownDisplayLocation).Format("January 2, 2006 at 15:04:05 MST")
}

func humanLabel(key string) string {
	words := strings.Fields(strings.ReplaceAll(key, "_", " "))
	for i, word := range words {
		if word == "id" {
			words[i] = "ID"
			continue
		}
		words[i] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

func titleText(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func codeList(values []string) string {
	if len(values) == 0 {
		return ""
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		out = append(out, fmt.Sprintf("`%s`", value))
	}
	return strings.Join(out, ", ")
}
