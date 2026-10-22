package tackprovider_test

import (
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"goodkind.io/tack/provider/internal/tackprovider"
)

const unitTokenSecret = "unit-test-token-secret"

type nodeValue struct {
	Endpoint types.String `tfsdk:"endpoint"`
	APIToken types.String `tfsdk:"api_token"`
	Insecure types.Bool   `tfsdk:"insecure"`
	NodeName types.String `tfsdk:"node_name"`
}

type operatorValue struct {
	Email   types.String `tfsdk:"email"`
	Name    types.String `tfsdk:"name"`
	Service types.String `tfsdk:"service"`
	Session types.String `tfsdk:"session"`
}

type providerValue struct {
	Nodes    types.Map      `tfsdk:"nodes"`
	Operator *operatorValue `tfsdk:"operator"`
}

var nodeObjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"endpoint":  types.StringType,
	"api_token": types.StringType,
	"insecure":  types.BoolType,
	"node_name": types.StringType,
}}

// TestConfigureDiagnosticsOmitToken calls Configure with an endpoint without an
// HTTP scheme and with a token that lacks the user, realm, and token ID. Each
// call must return the expected error without the token secret. The test does
// not require TF_ACC or a network connection.
func TestConfigureDiagnosticsOmitToken(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		token    string
		want     string
	}{
		{
			name:     "endpoint without an HTTP scheme",
			endpoint: "ftp://[2001:db8::1]:8006",
			token:    "acceptance@pve!unit=" + unitTokenSecret,
			want:     "endpoint must start with https:// or http://",
		},
		{
			name:     "token without the user, realm, and token ID",
			endpoint: "https://[2001:db8::1]:8006",
			token:    unitTokenSecret,
			want:     "api_token must have the form user@realm!tokenid=secret",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			text := configureText(t, nodeValue{
				Endpoint: types.StringValue(testCase.endpoint),
				APIToken: types.StringValue(testCase.token),
				Insecure: types.BoolValue(false),
				NodeName: types.StringValue("hypervisor"),
			})
			if strings.Contains(text, unitTokenSecret) {
				t.Fatal("the Configure diagnostics contain the API token secret")
			}
			if !strings.Contains(text, testCase.want) {
				t.Fatalf("Configure diagnostics = %q, want %q", text, testCase.want)
			}
		})
	}
}

func configureText(t *testing.T, node nodeValue) string {
	t.Helper()
	ctx := t.Context()
	tackProvider := tackprovider.New("test")()
	var schemaResp provider.SchemaResponse
	tackProvider.Schema(ctx, provider.SchemaRequest{}, &schemaResp)
	nodes, diags := types.MapValueFrom(ctx, nodeObjectType, map[string]nodeValue{"suburban": node})
	plan := tfsdk.Plan{
		Schema: schemaResp.Schema,
		Raw:    tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil),
	}
	diags.Append(plan.Set(ctx, &providerValue{
		Nodes: nodes,
		Operator: &operatorValue{
			Email:   types.StringValue("operator@example.com"),
			Name:    types.StringValue("unit test"),
			Service: types.StringNull(),
			Session: types.StringNull(),
		},
	})...)
	if diags.HasError() {
		t.Fatalf("build the provider configuration: %s", diagnosticsText(diags))
	}
	var resp provider.ConfigureResponse
	tackProvider.Configure(ctx, provider.ConfigureRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: plan.Raw},
	}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Configure succeeded, want an error")
	}
	return diagnosticsText(resp.Diagnostics)
}
