package tackprovider

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

var absolutePathPattern = regexp.MustCompile(`^/`)

// schemaResource is tack_schema: the migration version of the Tack ledger
// database of one installation.
type schemaResource struct {
	data *providerData
}

var (
	_ resource.ResourceWithConfigure   = (*schemaResource)(nil)
	_ resource.ResourceWithImportState = (*schemaResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*schemaResource)(nil)
)

func newSchemaResource() resource.Resource {
	return &schemaResource{data: nil}
}

func (r *schemaResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_schema"
}

func (r *schemaResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	migrationObject := schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
		"version": schema.Int64Attribute{Computed: true, Description: "Migration version."},
		"name":    schema.StringAttribute{Computed: true, Description: "Migration file name."},
		"sha256":  schema.StringAttribute{Computed: true, Description: "SHA-256 of the migration file."},
	}}
	resp.Schema = schema.Schema{
		Description: "The migration version of the Tack ledger database. Apply runs `migrate --execute` " +
			"in the tack-ops service. Delete removes the resource from state and reverts no migration.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "The installation in the form <node>/<vmid>:<install_dir>.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"node": schema.StringAttribute{
				Required:      true,
				Description:   "Key of the provider nodes map for the hypervisor that runs the guest.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"vmid": schema.Int64Attribute{
				Required:      true,
				Description:   "Proxmox ID of the Tack container.",
				Validators:    []validator.Int64{int64validator.AtLeast(1)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"install_dir": schema.StringAttribute{
				Required:    true,
				Description: "Absolute path of the Tack compose project in the guest.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(absolutePathPattern, "must be an absolute path"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"version": schema.Int64Attribute{
				Required:    true,
				Description: "Target migration version. Refresh sets it to the applied version.",
				Validators:  []validator.Int64{int64validator.AtLeast(1)},
			},
			"applied_version": schema.Int64Attribute{
				Computed:    true,
				Description: "Highest applied migration version in the database.",
			},
			"image_commit": schema.StringAttribute{
				Computed:    true,
				Description: "Commit of the tack-ops image that embeds the available migrations.",
			},
			"available": schema.ListNestedAttribute{
				Computed:     true,
				Description:  "Migrations that the tack-ops image embeds, in version order.",
				NestedObject: migrationObject,
			},
			"pending": schema.ListNestedAttribute{
				Computed: true,
				Description: "Migrations above the applied version through version. A plan that changes " +
					"version shows this list as known after apply and lists the migrations in a warning.",
				NestedObject: migrationObject,
			},
		},
	}
}

func (r *schemaResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			fmt.Sprintf("tack_schema received provider data of type %T.", req.ProviderData))
		return
	}
	r.data = data
}

func (r *schemaResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	node, vmid, installDir, err := parseSchemaID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), schemaID(node, vmid, installDir))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("node"), node)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vmid"), vmid)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("install_dir"), installDir)...)
}

// configured reports whether the provider configured this resource, and adds
// an error diagnostic when it did not.
func (r *schemaResource) configured(diags *diag.Diagnostics) bool {
	if r.data != nil {
		return true
	}
	diags.AddError("Unconfigured provider", "The tack provider was not configured before tack_schema ran.")
	return false
}
