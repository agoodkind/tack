package tackprovider

import (
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// operatorNamespace is the version 5 UUID namespace that the configs repository
// uses for deploy_operator_id in ansible/inventory/group_vars/tack_all.yml.
var operatorNamespace = uuid.MustParse("0a6f7572-cafe-dead-beef-000000000004")

type operatorModel struct {
	Email   types.String `tfsdk:"email"`
	Name    types.String `tfsdk:"name"`
	Service types.String `tfsdk:"service"`
	Session types.String `tfsdk:"session"`
}

func operatorBlock() schema.SingleNestedBlock {
	return schema.SingleNestedBlock{
		Description: "Every Tack command records this operator identity in the audit ledger.",
		Attributes: map[string]schema.Attribute{
			"email": schema.StringAttribute{
				Optional: true,
				Description: "This email identifies the accountable person. The provider rejects an empty email " +
					"and derives the operator ID as the version 5 UUID of the lowercased email in the " +
					"namespace that deploy_operator_id uses.",
			},
			"name": schema.StringAttribute{
				Optional:    true,
				Description: "This value is the name of the accountable person. Tack commands receive the name as --operator-name when service is empty.",
			},
			"service": schema.StringAttribute{
				Optional:    true,
				Description: "This agent service name, such as claude-rowan, requires a session.",
			},
			"session": schema.StringAttribute{
				Optional:    true,
				Description: "This agent session identifier requires a service.",
			},
		},
	}
}

// operatorFlags returns operator identity flags in the format that the configs
// variable tack_ops_identity_flags renders.
func operatorFlags(model *operatorModel) ([]string, error) {
	if model == nil {
		return nil, errors.New("the provider configuration requires the operator block")
	}
	if model.Email.IsUnknown() || model.Name.IsUnknown() || model.Service.IsUnknown() || model.Session.IsUnknown() {
		return nil, errors.New("the operator arguments must be known when the provider runs Configure")
	}
	email := strings.ToLower(model.Email.ValueString())
	if strings.TrimSpace(email) == "" {
		return nil, errors.New("operator.email is empty. The operator configuration requires the accountable person's email")
	}
	service := model.Service.ValueString()
	session := model.Session.ValueString()
	if service == "" && session != "" {
		return nil, errors.New("operator.session requires operator.service")
	}
	if service != "" && session == "" {
		return nil, errors.New("operator.service requires operator.session")
	}
	operatorID := uuid.NewSHA1(operatorNamespace, []byte(email)).String()
	if service != "" {
		return []string{
			"--operator-service", service,
			"--operator-session", session,
			"--operator-id", operatorID,
			"--operator-email", email,
		}, nil
	}
	return []string{
		"--operator-id", operatorID,
		"--operator-email", email,
		"--operator-name", model.Name.ValueString(),
	}, nil
}
