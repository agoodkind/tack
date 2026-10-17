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
		Description: "The operator identity that every Tack command records in the audit ledger.",
		Attributes: map[string]schema.Attribute{
			"email": schema.StringAttribute{
				Optional: true,
				Description: "Email of the accountable person. The provider refuses an empty email. " +
					"The operator ID is the version 5 UUID of the lowercased email.",
			},
			"name": schema.StringAttribute{
				Optional:    true,
				Description: "Name of the accountable person. Commands receive it when service is empty.",
			},
			"service": schema.StringAttribute{
				Optional:    true,
				Description: "Agent service name, such as claude-rowan. A service requires a session.",
			},
			"session": schema.StringAttribute{
				Optional:    true,
				Description: "Agent session identifier. A session requires a service.",
			},
		},
	}
}

// operatorFlags returns the operator identity flags in the form that the
// configs variable tack_ops_identity_flags renders.
func operatorFlags(model *operatorModel) ([]string, error) {
	if model == nil {
		return nil, errors.New("the operator block is required")
	}
	if model.Email.IsUnknown() || model.Name.IsUnknown() || model.Service.IsUnknown() || model.Session.IsUnknown() {
		return nil, errors.New("the operator arguments must be known when the provider is configured")
	}
	email := strings.ToLower(model.Email.ValueString())
	if strings.TrimSpace(email) == "" {
		return nil, errors.New("operator.email is empty; set the email of the accountable person")
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
