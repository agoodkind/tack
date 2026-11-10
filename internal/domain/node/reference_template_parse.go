package node

import (
	"fmt"
	"strings"
)

// ReferenceParseResult contains the values that parsing recovers from a
// rendered reference.
type ReferenceParseResult struct {
	// ScopeRefs maps feature names to their parsed scope references.
	ScopeRefs map[string]string
	// Props maps property names to their parsed values.
	Props map[string]string
	// NodeTypeKey contains the parsed node type when the template declares one.
	NodeTypeKey string
}

// Parse recovers a template's variable parts from input. User-typed references
// can fail to match the template.
//
// Errors include the input, template, and failure context as text. Parse does
// not log failures; callers decide how to handle a reference that does not
// match.
//
//   - Scope reference parts populate ScopeRefs.
//   - Property parts populate Props.
//   - Node type parts populate NodeTypeKey.
func (t ReferenceTemplate) Parse(input string) (ReferenceParseResult, error) {
	result := ReferenceParseResult{
		ScopeRefs:   map[string]string{},
		Props:       map[string]string{},
		NodeTypeKey: "",
	}
	remaining := input

	for index, part := range t.Parts {
		if part.Kind == ReferencePartLiteral {
			if index == 0 || t.Parts[index-1].Kind == ReferencePartLiteral {
				if !strings.HasPrefix(remaining, part.Value) {
					return ReferenceParseResult{}, fmt.Errorf(
						"parse %q against template %q: separator %q not found",
						input,
						t.Name,
						part.Value,
					)
				}

				remaining = strings.TrimPrefix(remaining, part.Value)
			}

			continue
		}

		segment, rest, err := consumeReferenceSegment(t.Parts, index, remaining)
		if err != nil {
			return ReferenceParseResult{}, fmt.Errorf(
				"parse %q against template %q: %s",
				input,
				t.Name,
				err.Error(),
			)
		}

		remaining = rest
		switch part.Kind {
		case ReferencePartScopeRef:
			result.ScopeRefs[part.Value] = segment
		case ReferencePartProperty:
			result.Props[part.Value] = segment
		case ReferencePartNodeType:
			result.NodeTypeKey = segment
		case ReferencePartLiteral:
		}
	}

	return result, nil
}

// consumeReferenceSegment returns the variable part at index and the text
// after its following delimiter.
//
// The cut reserves enough delimiter occurrences for the remaining template
// parts. Later variable parts may contain the delimiter themselves. The
// earliest variable part includes every surplus occurrence. Parsing
// "FAN-QA-13" against scope "-" sequence yields scope "FAN-QA". Parsing
// "FAN-epic-1" against scope "-" type "-" sequence yields scope "FAN", type
// "epic", and sequence "1".
func consumeReferenceSegment(
	parts []ReferencePart,
	index int,
	remaining string,
) (string, string, error) {
	delimiter := nextReferenceLiteral(parts, index)
	if delimiter == "" {
		if remaining == "" {
			return "", "", fmt.Errorf("no text left for final part")
		}

		return remaining, "", nil
	}

	reserved := laterLiteralCount(parts, index, delimiter)
	cut := nthIndexFromEnd(remaining, delimiter, reserved+1)
	if cut < 0 {
		return "", "", fmt.Errorf("separator %q not found", delimiter)
	}

	if cut == 0 {
		return "", "", fmt.Errorf("no text before separator %q", delimiter)
	}

	return remaining[:cut], remaining[cut+len(delimiter):], nil
}

// laterLiteralCount counts literal parts equal to delimiter that appear after
// the next literal following index. Each one reserves a delimiter occurrence
// at the end of the input.
func laterLiteralCount(parts []ReferencePart, index int, delimiter string) int {
	passedNext := false
	count := 0

	for cursor := index + 1; cursor < len(parts); cursor++ {
		if parts[cursor].Kind != ReferencePartLiteral {
			continue
		}

		if !passedNext {
			passedNext = true
			continue
		}

		if parts[cursor].Value == delimiter {
			count++
		}
	}

	return count
}

// nthIndexFromEnd returns the byte index of the nth occurrence of needle in
// text, counted from the end, or -1 when fewer than n occurrences exist.
func nthIndexFromEnd(text, needle string, n int) int {
	end := len(text)
	position := -1

	for range n {
		position = strings.LastIndex(text[:end], needle)
		if position < 0 {
			return -1
		}

		end = position
	}

	return position
}

func nextReferenceLiteral(parts []ReferencePart, index int) string {
	for cursor := index + 1; cursor < len(parts); cursor++ {
		if parts[cursor].Kind == ReferencePartLiteral {
			return parts[cursor].Value
		}
	}

	return ""
}
