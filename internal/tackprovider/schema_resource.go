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

// schemaResource implements tack_schema, which manages the migration version of
// one Tack installation's ledger database.
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
		"version": schema.Int64Attribute{Computed: true, Description: "This value identifies the migration version."},
		"name":    schema.StringAttribute{Computed: true, Description: "This value specifies the migration file name."},
		"sha256": schema.StringAttribute{
			Computed: true, Description: "This value contains the SHA-256 hash of the migration file.",
		},
	}}
	resp.Schema = schema.Schema{
		Description: "tack_schema manages the migration version of one Tack installation's ledger database, " +
			"YugabyteDB. Apply runs migrate --execute in the tack-ops service. Delete removes the resource " +
			"from state and does not revert migrations.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "This ID identifies the installation in the form `<node>/<vmid>:<install_dir>`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"node": schema.StringAttribute{
				Required:      true,
				Description:   "This key selects the hypervisor that runs the guest from the provider nodes map.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"vmid": schema.Int64Attribute{
				Required:      true,
				Description:   "This Proxmox ID identifies the Tack container.",
				Validators:    []validator.Int64{int64validator.AtLeast(1)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"install_dir": schema.StringAttribute{
				Required:    true,
				Description: "This absolute path identifies the Tack compose project directory in the guest.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(absolutePathPattern, "must be an absolute path"),
				},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"version": schema.Int64Attribute{
				Required:    true,
				Description: "This value sets the target migration version. Refresh sets version to the applied version.",
				Validators:  []validator.Int64{int64validator.AtLeast(1)},
			},
			"applied_version": schema.Int64Attribute{
				Computed:    true,
				Description: "This value reports the highest applied migration version in the database.",
			},
			"image_commit": schema.StringAttribute{
				Computed:    true,
				Description: "This value identifies the commit of the tack-ops image that embeds the available migrations.",
			},
			"available": schema.ListNestedAttribute{
				Computed:     true,
				Description:  "This list contains the migrations that the tack-ops image embeds, in version order.",
				NestedObject: migrationObject,
			},
			"pending": schema.ListNestedAttribute{
				Computed: true,
				Description: "This list contains migrations above the applied version through version. When a plan " +
					"changes version, it shows pending as known after apply and lists each migration in a warning.",
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

// configured reports whether the provider configured this resource. It adds an
// error diagnostic if the provider did not configure it.
func (r *schemaResource) configured(diags *diag.Diagnostics) bool {
	if r.data != nil {
		return true
	}
	diags.AddError("Unconfigured provider", "The tack provider did not configure tack_schema before the resource ran.")
	return false
}
