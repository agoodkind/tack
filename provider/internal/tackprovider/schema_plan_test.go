package tackprovider_test

import (
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

	"goodkind.io/tack/provider/internal/tackprovider"
)

// guardedVersion is the minimum applied version required for a target at or
// below this version.
const guardedVersion = 19

// planVersion plans tack_schema at target for an installation that state
// records at the applied version in status. It returns the planned
// applied_version, the planned pending list, and the diagnostics.
func planVersion(t *testing.T, status migrationStatus, target int64) (types.Int64, types.List, diag.Diagnostics) {
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
	highest := statusDocument(t, 0).highest()
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
			want: fmt.Sprintf("version %d exceeds the highest available migration version %d", highest+1, highest),
		},
		{
			name: "at the guarded version while the applied version is below it", applied: 17, target: guardedVersion,
			want: fmt.Sprintf("version %d is at or below version %d. The applied migration version is 17",
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
	status := statusDocument(t, statusDocument(t, 0).highest())
	applied, pending, diags := planVersion(t, status, status.AppliedVersion)
	if len(diags) != 0 {
		t.Fatalf("plan diagnostics = %q, want none", diagnosticsText(diags))
	}
	if applied.ValueInt64() != status.AppliedVersion || pending.IsUnknown() || len(pending.Elements()) != 0 {
		t.Fatalf("planned applied_version %v and pending %v, want %d and an empty list",
			applied, pending, status.AppliedVersion)
	}
}
