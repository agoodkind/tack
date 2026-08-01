package datagen

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

type soakDiscoveryMCP struct {
	renderedLabelGets    []string
	labelCreateArguments []ToolArguments
}

func (f *soakDiscoveryMCP) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	var payload rpcRequest
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	args := payload.Params.Arguments
	switch payload.Params.Name {
	case "tack_list_projects":
		writeDiscoveryList(writer, "projects", "Q0101", "QA Project 1.1 seed 777")
	case "tack_list_labels":
		writeDiscoveryListItems(writer, "labels", []collectionItem{
			{
				Reference: "qa-777-o01-w01::developer-experience-01-w01-seed-777",
				Name:      "developer-experience-01-w01-seed-777",
			},
			{
				Reference: "qa-777-o01-w01::Soak label op-00000007 seed-777",
				Name:      "Soak label op-00000007 seed-777",
			},
		})
	case "tack_list_states":
		writeDiscoveryList(writer, "states", "Q0101::Todo", "Todo")
	case "tack_list_issues":
		writeDiscoveryList(writer, "issues", "Q0101-1", "Existing issue")
	case "tack_list_epics":
		writeDiscoveryList(writer, "epics", "Q0101-2", "Existing epic")
	case "tack_list_cycles":
		writeDiscoveryList(writer, "cycles", "Q0101-3", "Existing cycle")
	case "tack_list_modules":
		writeDiscoveryList(writer, "modules", "Q0101-4", "Existing module")
	case "tack_get_label":
		if _, err := uuid.Parse(args.NodeID); err != nil {
			f.renderedLabelGets = append(f.renderedLabelGets, args.NodeID)
			writeRerunResult(writer, "not found", true)
			return
		}
		writeDiscoveryNode(writer, deterministicUUID("label").String(), "", 0)
	case "tack_create_label":
		f.labelCreateArguments = append(f.labelCreateArguments, args)
		writeDiscoveryNode(writer, deterministicUUID(args.Name).String(), "", 0)
	case "tack_get_project":
		writeDiscoveryNode(writer, deterministicUUID("project").String(), "", 0)
	case "tack_get_state":
		writeDiscoveryNode(writer, deterministicUUID("state").String(), "unstarted", 10)
	case "tack_get_issue":
		writeDiscoveryNode(writer, deterministicUUID("issue").String(), "", 0)
	case "tack_get_epic":
		writeDiscoveryNode(writer, deterministicUUID("epic").String(), "", 0)
	case "tack_get_cycle":
		writeDiscoveryNode(writer, deterministicUUID("cycle").String(), "", 0)
	case "tack_get_module":
		writeDiscoveryNode(writer, deterministicUUID("module").String(), "", 0)
	default:
		http.Error(writer, "unexpected tool "+payload.Params.Name, http.StatusBadRequest)
	}
}

func writeDiscoveryList(writer http.ResponseWriter, plural, reference, name string) {
	writeDiscoveryListItems(writer, plural, []collectionItem{{Reference: reference, Name: name}})
}

func writeDiscoveryListItems(
	writer http.ResponseWriter,
	plural string,
	items []collectionItem,
) {
	var builder strings.Builder
	fmt.Fprintf(
		&builder,
		"#### %s\n\n%d %s found.\n",
		strings.ToUpper(plural[:1])+plural[1:],
		len(items),
		plural,
	)
	for _, item := range items {
		fmt.Fprintf(
			&builder,
			"- `%s`\n  - Name: %s\n  - Type: `%s`\n  - Assigned: unassigned\n",
			item.Reference,
			item.Name,
			discoveryNodeType(plural),
		)
	}
	writeRerunResult(writer, builder.String(), false)
}

func discoveryListText(plural, reference, name string) string {
	var builder strings.Builder
	writeDiscoveryListText(&builder, plural, []collectionItem{{
		Reference: reference,
		Name:      name,
	}})
	return builder.String()
}

func writeDiscoveryListText(
	builder *strings.Builder,
	plural string,
	items []collectionItem,
) {
	fmt.Fprintf(
		builder,
		"#### %s\n\n%d %s found.\n",
		strings.ToUpper(plural[:1])+plural[1:],
		len(items),
		plural,
	)
	for _, item := range items {
		fmt.Fprintf(
			builder,
			"- `%s`\n  - Name: %s\n  - Type: `%s`\n  - Assigned: unassigned\n",
			item.Reference,
			item.Name,
			discoveryNodeType(plural),
		)
	}
}

func discoveryNodeType(plural string) string {
	if plural == "activities" {
		return "activity"
	}
	return strings.TrimSuffix(plural, "s")
}

func writeDiscoveryNode(writer http.ResponseWriter, rawID, group string, sortOrder int) {
	text := fmt.Sprintf("- Raw id: `%s`\n", rawID)
	if group != "" {
		text += fmt.Sprintf("- Group: %s\n- Sort order: %d\n", group, sortOrder)
	}
	writeRerunResult(writer, text, false)
}
