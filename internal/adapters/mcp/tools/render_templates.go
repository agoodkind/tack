package tools

import (
	"bytes"
	"embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed templates/*.md.tmpl
var markdownTemplateFS embed.FS

var markdownTemplates = template.Must(template.New("").ParseFS(markdownTemplateFS, "templates/*.md.tmpl"))

type markdownField struct {
	Label string
	Value string
}

type markdownItem struct {
	Title  string
	Fields []markdownField
}

type nodeTemplateData struct {
	Heading string
	Fields  []markdownField
}

type collectionTemplateData struct {
	Heading string
	Count   int
	Noun    string
	Items   []markdownItem
}

type rawJSONTemplateData struct {
	Heading string
	Intro   string
	Body    string
}

type workspaceDescribeTemplateData struct {
	Node      string
	NodeTypes []markdownItem
	Children  string
}

func executeMarkdownTemplate(name string, data any) string {
	var buf bytes.Buffer
	if err := markdownTemplates.ExecuteTemplate(&buf, name, data); err != nil {
		panic(fmt.Sprintf("render markdown template %s: %v", name, err))
	}
	return strings.TrimRight(buf.String(), "\n") + "\n"
}

func markdownValue(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func markdownCodeValue(value string) string {
	if value == "" {
		return "-"
	}
	return fmt.Sprintf("`%s`", value)
}

func markdownFieldValue(label string, value string) markdownField {
	return markdownField{Label: label, Value: markdownValue(value)}
}

func markdownCodeFieldValue(label string, value string) markdownField {
	return markdownField{Label: label, Value: markdownCodeValue(value)}
}
