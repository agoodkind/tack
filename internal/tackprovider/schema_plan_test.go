package tackprovider_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"goodkind.io/tack/internal/response"
	"goodkind.io/tack/internal/schemastatus"
	"goodkind.io/tack/internal/tackprovider"
	"goodkind.io/tack/migrations"
)

// guardedVersion is the version that a target at or below requires as the
// applied version.
const guardedVersion = 19

type migrationValue struct {
	Version types.Int64  `tfsdk:"version"`
	Name    types.String `tfsdk:"name"`
	SHA256  types.String `tfsdk:"sha256"`
}

var migrationObjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"version": types.Int64Type,
	"name":    types.StringType,
	"sha256":  types.StringType,
}}

// statusDocument encodes the status of a database at applied with the embedded
// migrations in the `migrate --output json` envelope and decodes the result.
func statusDocument(t *testing.T, applied int64) schemastatus.Status {
	t.Helper()
	available, err := schemastatus.Available(t.Context(), migrations.FS)
	if err != nil {
		t.Fatalf("list the embedded migrations: %v", err)
	}
	pending := make([]schemastatus.Migration, 0, len(available))
	for _, embedded := range available {
		if embedded.Version > applied {
			pending = append(pending, embedded)
		}
	}
	payload, err := json.Marshal(schemastatus.Status{
		MigrationTablePresent: true,
		AppliedVersion:        applied,
		Available:             available,
		Pending:               pending,
		ImageCommit:           "plan-test",
	})
	if err != nil {
		t.Fatalf("encode the status: %v", err)
	}
	body, err := response.Marshal(t.Context(), payload)
	if err != nil {
		t.Fatalf("wrap the status in the output envelope: %v", err)
	}
	var document response.JSONDocument
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode the output envelope: %v", err)
	}
	var status schemastatus.Status
	if err := json.Unmarshal(document.Result, &status); err != nil {
		t.Fatalf("decode the status document: %v", err)
	}
	return status
}

func migrationList(t *testing.T, embedded []schemastatus.Migration) types.List {
	t.Helper()
	values := make([]migrationValue, 0, len(embedded))
	for _, item := range embedded {
		values = append(values, migrationValue{
			Version: types.Int64Value(item.Version),
			Name:    types.StringValue(item.Name),
			SHA256:  types.StringValue(item.SHA256),
		})
	}
	list, diags := types.ListValueFrom(t.Context(), migrationObjectType, values)
	if diags.HasError() {
		t.Fatalf("build the migration list: %s", diagnosticsText(diags))
	}
	return list
}

// planVersion runs the tack_schema plan of version target for an installation
// that state records at the applied version of status. It returns the planned
// applied_version, the planned pending list, and the diagnostics.
func planVersion(t *testing.T, status schemastatus.Status, target int64) (types.Int64, types.List, diag.Diagnostics) {
	t.Helper()
	ctx := t.Context()
	schemaResource := tackprovider.New("test")().Resources(ctx)[0]()
	modifier, ok := schemaResource.(resource.ResourceWithModifyPlan)
	if !ok {
		t.Fatal("tack_schema does not modify plans")
	}
	var schemaResp resource.SchemaResponse
	schemaResource.Schema(ctx, resource.SchemaRequest{}, &schemaResp)

	state := tfsdk.State{Schema: schemaResp.Schema, Raw: tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil)}
	recorded := map[string]attr.Value{
		"id":              types.StringValue("suburban/217:/root/tack"),
		"node":            types.StringValue("suburban"),
		"vmid":            types.Int64Value(217),
		"install_dir":     types.StringValue("/root/tack"),
		"version":         types.Int64Value(status.AppliedVersion),
		"applied_version": types.Int64Value(status.AppliedVersion),
		"image_commit":    types.StringValue(status.ImageCommit),
		"available":       migrationList(t, status.Available),
		"pending":         migrationList(t, nil),
	}
	var diags diag.Diagnostics
	for name, value := range recorded {
		diags.Append(state.SetAttribute(ctx, path.Root(name), value)...)
	}
	plan := tfsdk.Plan{Schema: schemaResp.Schema, Raw: state.Raw.Copy()}
	diags.Append(plan.SetAttribute(ctx, path.Root("version"), types.Int64Value(target))...)
	if diags.HasError() {
		t.Fatalf("build the plan request: %s", diagnosticsText(diags))
	}

	req := resource.ModifyPlanRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: plan.Raw},
		State:  state,
		Plan:   plan,
	}
	resp := resource.ModifyPlanResponse{Plan: plan}
	modifier.ModifyPlan(ctx, req, &resp)
	var applied types.Int64
	var pending types.List
	resp.Diagnostics.Append(resp.Plan.GetAttribute(ctx, path.Root("applied_version"), &applied)...)
	resp.Diagnostics.Append(resp.Plan.GetAttribute(ctx, path.Root("pending"), &pending)...)
	return applied, pending, resp.Diagnostics
}

func diagnosticsText(diags diag.Diagnostics) string {
	parts := make([]string, 0, len(diags))
	for _, diagnostic := range diags {
		parts = append(parts, diagnostic.Summary()+": "+diagnostic.Detail())
	}
	return strings.Join(parts, "; ")
}

func TestPlanRejectsInvalidVersions(t *testing.T) {
	highest := statusDocument(t, 0).Highest()
	if highest < guardedVersion {
		t.Fatalf("the image embeds migrations through %d, want at least %d", highest, guardedVersion)
	}
	cases := []struct {
		name    string
		applied int64
		target  int64
		want    string
	}{
		{
			name: "below the applied version", applied: highest, target: highest - 1,
			want: fmt.Sprintf("version %d is below the applied migration version %d", highest-1, highest),
		},
		{
			name: "above the highest available version", applied: highest, target: highest + 1,
			want: fmt.Sprintf("version %d is above the highest available migration version %d", highest+1, highest),
		},
		{
			name: "at the guarded version while the applied version is below it", applied: 17, target: guardedVersion,
			want: fmt.Sprintf("version %d is at or below version %d, and the applied migration version is 17",
				guardedVersion, guardedVersion),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, diags := planVersion(t, statusDocument(t, testCase.applied), testCase.target)
			if !diags.HasError() {
				t.Fatalf("plan of version %d at applied version %d succeeded, want an error", testCase.target, testCase.applied)
			}
			if text := diagnosticsText(diags); !strings.Contains(text, testCase.want) {
				t.Fatalf("plan diagnostics = %q, want %q", text, testCase.want)
			}
		})
	}
}

func TestPlanAtTheAppliedVersionChangesNothing(t *testing.T) {
	status := statusDocument(t, statusDocument(t, 0).Highest())
	applied, pending, diags := planVersion(t, status, status.AppliedVersion)
	if len(diags) != 0 {
		t.Fatalf("plan diagnostics = %q, want none", diagnosticsText(diags))
	}
	if applied.ValueInt64() != status.AppliedVersion || pending.IsUnknown() || len(pending.Elements()) != 0 {
		t.Fatalf("planned applied_version %v and pending %v, want %d and an empty list",
			applied, pending, status.AppliedVersion)
	}
}
