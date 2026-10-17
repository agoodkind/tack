package tackprovider

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ModifyPlan sets the computed values from the migration status and rejects a
// version that the status does not allow. A plan for the installation in state
// uses the refreshed state. A plan for a new installation reads the live
// status with the read-only command.
func (r *schemaResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan schemaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.Node.IsUnknown() || plan.VMID.IsUnknown() || plan.InstallDir.IsUnknown() || plan.Version.IsUnknown() {
		return
	}
	status, known := r.plannedStatus(ctx, req, plan, &resp.Diagnostics)
	if !known || resp.Diagnostics.HasError() {
		return
	}
	target := plan.Version.ValueInt64()
	if problem := status.targetProblem(target); problem != "" {
		slog.ErrorContext(ctx, "schema.plan_rejected", slog.Int64("version", target),
			slog.Int64("applied_version", status.AppliedVersion), slog.String("err", problem))
		resp.Diagnostics.AddAttributeError(path.Root("version"), "Invalid tack_schema version", problem)
		return
	}
	planned, diags := modelFromStatus(ctx, plan, status, target)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	planned.AppliedVersion = types.Int64Value(target)
	if target != status.AppliedVersion {
		planned.Pending = types.ListUnknown(migrationObjectType)
		resp.Diagnostics.AddAttributeWarning(path.Root("version"), "Pending Tack migrations",
			fmt.Sprintf("Apply runs these migrations in %s from version %d through version %d:\n%s",
				plan.target().guest, status.AppliedVersion, target, pendingText(status.pendingThrough(target))))
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &planned)...)
}

// plannedStatus returns the migration status that the plan compares with. It
// returns false when the status cannot be known during this plan.
func (r *schemaResource) plannedStatus(
	ctx context.Context,
	req resource.ModifyPlanRequest,
	plan schemaModel,
	diags *diag.Diagnostics,
) (schemaStatus, bool) {
	var unknown schemaStatus
	if !req.State.Raw.IsNull() {
		var state schemaModel
		diags.Append(req.State.Get(ctx, &state)...)
		if diags.HasError() {
			return unknown, false
		}
		if state.sameTarget(plan) {
			status, stateDiags := statusFromModel(ctx, state)
			diags.Append(stateDiags...)
			return status, !diags.HasError()
		}
	}
	if r.data == nil {
		return unknown, false
	}
	status, readDiags := r.data.readStatus(ctx, plan.target())
	diags.Append(readDiags...)
	return status, !diags.HasError()
}
