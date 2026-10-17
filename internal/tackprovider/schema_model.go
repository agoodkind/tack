package tackprovider

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/agoodkind/terraform-provider-pveguest/transport"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type schemaModel struct {
	ID             types.String `tfsdk:"id"`
	Node           types.String `tfsdk:"node"`
	VMID           types.Int64  `tfsdk:"vmid"`
	InstallDir     types.String `tfsdk:"install_dir"`
	Version        types.Int64  `tfsdk:"version"`
	AppliedVersion types.Int64  `tfsdk:"applied_version"`
	ImageCommit    types.String `tfsdk:"image_commit"`
	Available      types.List   `tfsdk:"available"`
	Pending        types.List   `tfsdk:"pending"`
}

type migrationModel struct {
	Version types.Int64  `tfsdk:"version"`
	Name    types.String `tfsdk:"name"`
	SHA256  types.String `tfsdk:"sha256"`
}

var migrationObjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"version": types.Int64Type,
	"name":    types.StringType,
	"sha256":  types.StringType,
}}

func (model schemaModel) target() schemaTarget {
	return schemaTarget{
		guest: transport.Guest{
			Node: model.Node.ValueString(),
			VMID: model.VMID.ValueInt64(),
			Kind: transport.KindLXC,
		},
		installDir: model.InstallDir.ValueString(),
	}
}

// sameTarget reports whether both models manage the same installation.
func (model schemaModel) sameTarget(other schemaModel) bool {
	return model.Node.Equal(other.Node) && model.VMID.Equal(other.VMID) && model.InstallDir.Equal(other.InstallDir)
}

// schemaID formats the resource ID and import ID as `<node>/<vmid>:<install_dir>`.
func schemaID(node string, vmid int64, installDir string) string {
	return fmt.Sprintf("%s/%d:%s", node, vmid, installDir)
}

// parseSchemaID splits an import ID in the form `<node>/<vmid>:<install_dir>`.
func parseSchemaID(id string) (string, int64, string, error) {
	guestPart, installDir, found := strings.Cut(id, ":")
	if !found || installDir == "" {
		return "", 0, "", fmt.Errorf("import ID %q does not match the form `<node>/<vmid>:<install_dir>`", id)
	}
	slash := strings.LastIndex(guestPart, "/")
	if slash <= 0 {
		return "", 0, "", fmt.Errorf("import ID %q does not match the form `<node>/<vmid>:<install_dir>`", id)
	}
	vmid, err := strconv.ParseInt(guestPart[slash+1:], 10, 64)
	if err != nil || vmid <= 0 {
		return "", 0, "", fmt.Errorf("import ID %q contains VMID %q, which is not a positive integer", id, guestPart[slash+1:])
	}
	return guestPart[:slash], vmid, installDir, nil
}

// migrationList converts migrations to a list value containing migration
// objects.
func migrationList(ctx context.Context, migrations []migration) (types.List, diag.Diagnostics) {
	models := make([]migrationModel, 0, len(migrations))
	for _, item := range migrations {
		models = append(models, migrationModel{
			Version: types.Int64Value(item.Version),
			Name:    types.StringValue(item.Name),
			SHA256:  types.StringValue(item.SHA256),
		})
	}
	return types.ListValueFrom(ctx, migrationObjectType, models)
}

// statusFromModel rebuilds migration status from the values in state.
func statusFromModel(ctx context.Context, model schemaModel) (schemaStatus, diag.Diagnostics) {
	var models []migrationModel
	diags := model.Available.ElementsAs(ctx, &models, false)
	available := make([]migration, 0, len(models))
	for _, item := range models {
		available = append(available, migration{
			Version: item.Version.ValueInt64(),
			Name:    item.Name.ValueString(),
			SHA256:  item.SHA256.ValueString(),
		})
	}
	return schemaStatus{
		MigrationTablePresent: true,
		AppliedVersion:        model.AppliedVersion.ValueInt64(),
		Available:             available,
		ImageCommit:           model.ImageCommit.ValueString(),
	}, diags
}

// modelFromStatus builds the installation state from identity, live status,
// and version. The pending list contains migrations above the applied version
// through version.
func modelFromStatus(
	ctx context.Context,
	identity schemaModel,
	status schemaStatus,
	version int64,
) (schemaModel, diag.Diagnostics) {
	available, diags := migrationList(ctx, status.Available)
	pending, pendingDiags := migrationList(ctx, status.pendingThrough(version))
	diags.Append(pendingDiags...)
	target := identity.target()
	return schemaModel{
		ID:             types.StringValue(schemaID(target.guest.Node, target.guest.VMID, target.installDir)),
		Node:           identity.Node,
		VMID:           identity.VMID,
		InstallDir:     identity.InstallDir,
		Version:        types.Int64Value(version),
		AppliedVersion: types.Int64Value(status.AppliedVersion),
		ImageCommit:    types.StringValue(status.ImageCommit),
		Available:      available,
		Pending:        pending,
	}, diags
}
