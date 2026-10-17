// Package tackprovider implements the Tack OpenTofu provider. Its resources
// run reviewed Tack commands in the Tack guest. The provider runs each command
// by calling the Proxmox VE API with the pveguest transport.
package tackprovider

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/agoodkind/terraform-provider-pveguest/transport"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const providerTypeName = "tack"

type tackProvider struct {
	version string
}

type providerModel struct {
	Nodes    types.Map      `tfsdk:"nodes"`
	Operator *operatorModel `tfsdk:"operator"`
}

type nodeModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	APIToken types.String `tfsdk:"api_token"`
	Insecure types.Bool   `tfsdk:"insecure"`
	NodeName types.String `tfsdk:"node_name"`
}

// providerData contains the values that Configure passes to every resource.
// Its operatorFlags field contains the identity flags for every Tack command.
type providerData struct {
	client        *transport.Client
	operatorFlags []string
}

// New returns the Tack provider factory and supplies version to OpenTofu as the
// provider version.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &tackProvider{version: version}
	}
}

func (p *tackProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = providerTypeName
	resp.Version = p.version
}

func (p *tackProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The provider manages the migration version of the ledger database of a Tack installation " +
			"with the tack_schema resource. It runs commands in the Tack guest by calling the Proxmox VE API " +
			"with the pveguest transport.",
		Attributes: map[string]schema.Attribute{
			"nodes": schema.MapNestedAttribute{
				Required: true,
				Description: "This map configures hypervisors by name using the pveguest provider nodes map " +
					"format. Each resource selects a hypervisor with its node argument.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"endpoint": schema.StringAttribute{
							Required:    true,
							Description: "This URL identifies the Proxmox VE API endpoint, for example https://[2001:db8::1]:8006.",
						},
						"api_token": schema.StringAttribute{
							Required:    true,
							Sensitive:   true,
							Description: "This API token uses the form user@realm!tokenid=secret.",
						},
						"node_name": schema.StringAttribute{
							Optional:    true,
							Description: "This Proxmox node name appears in the API path and defaults to the map key.",
							Validators:  []validator.String{stringvalidator.LengthAtLeast(1)},
						},
						"insecure": schema.BoolAttribute{
							Optional:    true,
							Description: "This option skips TLS certificate verification and defaults to false.",
						},
					},
				},
			},
		},
		Blocks: map[string]schema.Block{
			"operator": operatorBlock(),
		},
	}
}

func (p *tackProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if config.Nodes.IsUnknown() {
		resp.Diagnostics.AddError("Unknown provider configuration",
			"The nodes argument must be known when the provider runs Configure.")
		return
	}
	flags, err := operatorFlags(config.Operator)
	if err != nil {
		slog.ErrorContext(ctx, "provider.configure_failed", slog.String("err", err.Error()))
		resp.Diagnostics.AddError("Invalid operator configuration", err.Error())
		return
	}
	nodes, err := nodeConfigs(ctx, config.Nodes)
	if err != nil {
		slog.ErrorContext(ctx, "provider.configure_failed", slog.String("err", err.Error()))
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}
	client, err := transport.NewClient(nodes, transport.DefaultMaxRequests)
	if err != nil {
		slog.ErrorContext(ctx, "provider.configure_failed", slog.String("err", err.Error()))
		resp.Diagnostics.AddError("Invalid provider configuration", err.Error())
		return
	}
	slog.InfoContext(ctx, "provider.configured", slog.Int("nodes", len(nodes)))
	resp.ResourceData = &providerData{client: client, operatorFlags: flags}
}

func nodeConfigs(ctx context.Context, nodesValue types.Map) (map[string]transport.NodeConfig, error) {
	nodeModels := make(map[string]nodeModel, len(nodesValue.Elements()))
	if diags := nodesValue.ElementsAs(ctx, &nodeModels, false); diags.HasError() {
		return nil, fmt.Errorf("read the nodes argument: %s", diagnosticsText(diags))
	}
	nodes := make(map[string]transport.NodeConfig, len(nodeModels))
	for name, model := range nodeModels {
		if model.Endpoint.IsUnknown() || model.APIToken.IsUnknown() || model.Insecure.IsUnknown() ||
			model.NodeName.IsUnknown() {
			return nil, fmt.Errorf(
				"the endpoint, api_token, insecure, and node_name arguments of node %q must be known "+
					"when the provider runs Configure", name)
		}
		nodes[name] = transport.NodeConfig{
			Endpoint: model.Endpoint.ValueString(),
			APIToken: model.APIToken.ValueString(),
			Insecure: model.Insecure.ValueBool(),
			NodeName: model.NodeName.ValueString(),
		}
	}
	return nodes, nil
}

func (p *tackProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{newSchemaResource}
}

func (p *tackProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}
