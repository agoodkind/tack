package tackprovider_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
)

// secretAbsentFromPlan fails when the JSON plan contains the token secret outside
// the root variables section. OpenTofu records the test's input variable values
// in that section; the provider does not write it.
type secretAbsentFromPlan struct {
	secret string
}

func (check secretAbsentFromPlan) CheckPlan(
	_ context.Context,
	req plancheck.CheckPlanRequest,
	resp *plancheck.CheckPlanResponse,
) {
	plan := *req.Plan
	plan.Variables = nil
	encoded, err := json.Marshal(plan)
	if err != nil {
		resp.Error = fmt.Errorf("encode the plan: %w", err)
		return
	}
	if strings.Contains(string(encoded), check.secret) {
		resp.Error = errors.New("the plan contains the API token secret")
	}
}

// secretAbsentFromState fails when the JSON state, including outputs, contains
// the token secret.
type secretAbsentFromState struct {
	secret string
}

func (check secretAbsentFromState) CheckState(
	_ context.Context,
	req statecheck.CheckStateRequest,
	resp *statecheck.CheckStateResponse,
) {
	encoded, err := json.Marshal(req.State)
	if err != nil {
		resp.Error = fmt.Errorf("encode the state: %w", err)
		return
	}
	if strings.Contains(string(encoded), check.secret) {
		resp.Error = errors.New("the state or its outputs contain the API token secret")
	}
}

// priorAppliedVersion fails unless the prior state of tack_schema.qa in the plan
// has applied_version equal to version. The plan refreshes before the check, and
// its prior state contains the live applied version.
type priorAppliedVersion struct {
	version int64
}

type appliedVersionValue struct {
	AppliedVersion json.Number `json:"applied_version"`
}

func (check priorAppliedVersion) CheckPlan(
	_ context.Context,
	req plancheck.CheckPlanRequest,
	resp *plancheck.CheckPlanResponse,
) {
	for _, change := range req.Plan.ResourceChanges {
		if change.Address != schemaAddress || change.Change == nil {
			continue
		}
		encoded, err := json.Marshal(change.Change.Before)
		if err != nil {
			resp.Error = fmt.Errorf("encode the prior state of %s: %w", schemaAddress, err)
			return
		}
		var before appliedVersionValue
		if err := json.Unmarshal(encoded, &before); err != nil {
			resp.Error = fmt.Errorf("decode the prior state of %s: %w", schemaAddress, err)
			return
		}
		if before.AppliedVersion.String() != strconv.FormatInt(check.version, 10) {
			resp.Error = fmt.Errorf("the plan read applied_version %s for %s, want %d",
				before.AppliedVersion, schemaAddress, check.version)
		}
		return
	}
	resp.Error = fmt.Errorf("the plan does not contain %s", schemaAddress)
}
