package tackprovider_test

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

const (
	missingInstallDir = "/nonexistent/tack-provider-acceptance"

	// inaccessibleVMID is 999999999, a VMID the acceptance token cannot use.
	// Every Proxmox API request for this VMID fails.
	inaccessibleVMID = 999999999
)

// TestSchemaDiagnosticsOmitToken plans a new tack_schema with a missing guest
// install directory and with a guest the token cannot use. Each plan must fail
// with the expected diagnostic. The diagnostic text must not contain the token
// secret.
func TestSchemaDiagnosticsOmitToken(t *testing.T) {
	environment := newAcceptanceEnvironment(t)
	missingDirectory := environment
	missingDirectory.installDir = missingInstallDir
	inaccessibleGuest := environment
	inaccessibleGuest.vmid = inaccessibleVMID
	cases := []struct {
		name        string
		environment acceptanceEnvironment
		want        *regexp.Regexp
	}{
		{
			name:        "missing install directory",
			environment: missingDirectory,
			want:        regexp.MustCompile(`Tack command failed`),
		},
		{
			name:        "inaccessible guest",
			environment: inaccessibleGuest,
			want:        regexp.MustCompile(`Proxmox API request failed|Cannot contact the Tack guest`),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			planFailed := false
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: providerFactories(),
				ErrorCheck: func(err error) error {
					if strings.Contains(err.Error(), environment.secret()) {
						return errors.New("the plan diagnostics contain the API token secret")
					}
					if !testCase.want.MatchString(err.Error()) {
						return err
					}
					planFailed = true
					return nil
				},
				Steps: []resource.TestStep{{
					Config:          acceptanceConfig,
					ConfigVariables: testCase.environment.variables(environment.appliedVersion),
					PlanOnly:        true,
				}},
			})
			if !planFailed {
				t.Fatalf("the plan succeeded, want an error that matches %q", testCase.want)
			}
		})
	}
}
