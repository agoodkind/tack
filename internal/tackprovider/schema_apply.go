package tackprovider

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

func (r *schemaResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan schemaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.configured(&resp.Diagnostics) {
		return
	}
	before, diags := r.data.readStatus(ctx, plan.target())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.ImageCommit.IsUnknown() && plan.ImageCommit.ValueString() != before.ImageCommit {
		resp.Diagnostics.AddError("Tack image changed after planning",
			fmt.Sprintf("The plan read image commit %s. The installation %s now runs image commit %s. Run the plan again.",
				plan.ImageCommit.ValueString(), plan.target().guest, before.ImageCommit))
		return
	}
	model, applied := r.apply(ctx, plan, before, &resp.Diagnostics)
	if applied {
		resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
	}
}

func (r *schemaResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan schemaModel
	var state schemaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !r.configured(&resp.Diagnostics) {
		return
	}
	before, diags := statusFromModel(ctx, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	model, applied := r.apply(ctx, plan, before, &resp.Diagnostics)
	if applied {
		resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
	}
}

// apply migrates from the applied version in before to the planned version,
// then reads status again. It returns the new state and true only if the
// database matches the planned version and does not have pending migrations
// through that version.
func (r *schemaResource) apply(
	ctx context.Context,
	plan schemaModel,
	before schemaStatus,
	diags *diag.Diagnostics,
) (schemaModel, bool) {
	target := plan.target()
	version := plan.Version.ValueInt64()
	if problem := before.targetProblem(version); problem != "" {
		diags.AddError("Invalid tack_schema version", problem)
		return plan, false
	}
	if version != before.AppliedVersion {
		slog.InfoContext(ctx, "schema.apply_started", slog.String("guest", target.guest.String()),
			slog.Int64("applied_version", before.AppliedVersion), slog.Int64("version", version))
		argv := applyArgv(target, r.data.operatorFlags, version, before.AppliedVersion)
		_, runDiags := r.data.runCommand(ctx, target, argv, applyTimeoutSeconds)
		diags.Append(runDiags...)
		if diags.HasError() {
			return plan, false
		}
	}
	after, readDiags := r.data.readStatus(ctx, target)
	diags.Append(readDiags...)
	if diags.HasError() {
		return plan, false
	}
	pending := after.pendingThrough(version)
	if after.AppliedVersion != version || len(pending) != 0 {
		problem := fmt.Sprintf("After apply, the installation %s has migration version %d instead of version %d "+
			"and has %d pending migrations.", target.guest, after.AppliedVersion, version, len(pending))
		slog.ErrorContext(ctx, "schema.apply_failed", slog.String("guest", target.guest.String()),
			slog.String("err", problem))
		diags.AddError("Tack schema does not match the planned version", problem)
		return plan, false
	}
	model, modelDiags := modelFromStatus(ctx, plan, after, version)
	diags.Append(modelDiags...)
	if diags.HasError() {
		return plan, false
	}
	slog.InfoContext(ctx, "schema.applied", slog.String("guest", target.guest.String()),
		slog.Int64("applied_version", after.AppliedVersion))
	return model, true
}

// Read sets version to the live applied version. It keeps the resource in
// state if status is unreadable and removes it if the goose migration table
// does not exist.
func (r *schemaResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state schemaModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !r.configured(&resp.Diagnostics) {
		return
	}
	status, diags := r.data.readStatus(ctx, state.target())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !status.MigrationTablePresent {
		slog.InfoContext(ctx, "schema.absent", slog.String("guest", state.target().guest.String()))
		resp.State.RemoveResource(ctx)
		return
	}
	model, modelDiags := modelFromStatus(ctx, state, status, status.AppliedVersion)
	resp.Diagnostics.Append(modelDiags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

// Delete removes the resource from state and does not revert migrations.
func (r *schemaResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state schemaModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	slog.InfoContext(ctx, "schema.forgotten", slog.String("guest", state.target().guest.String()),
		slog.Int64("applied_version", state.AppliedVersion.ValueInt64()))
	resp.Diagnostics.AddWarning("Delete does not revert migrations",
		fmt.Sprintf("tack_schema removed %s from state only. The database remains at migration version %d.",
			state.ID.ValueString(), state.AppliedVersion.ValueInt64()))
}
