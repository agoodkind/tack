//go:build macrunnerfixture && runtimeaudit

package integration

import (
	"testing"

	"goodkind.io/tack/internal/testenv"
)

func TestMacOwnershipAll(t *testing.T)         { testenv.MacRunnerOwnershipFixture(t, "all") }
func TestMacOwnershipRelease(t *testing.T)     { testenv.MacRunnerOwnershipFixture(t, "release") }
func TestMacOwnershipUnknown(t *testing.T)     { testenv.MacRunnerOwnershipFixture(t, "unknown") }
func TestMacOwnershipShared(t *testing.T)      { testenv.MacRunnerOwnershipFixture(t, "shared") }
func TestMacOwnershipWait(t *testing.T)        { testenv.MacRunnerOwnershipFixture(t, "wait") }
func TestMacOwnershipMutation(t *testing.T)    { testenv.MacRunnerOwnershipFixture(t, "mutation") }
func TestMacOwnershipIndependent(t *testing.T) { testenv.MacRunnerOwnershipFixture(t, "independent") }

func TestMacOwnershipMissingAdmission(t *testing.T) {
	testenv.MacRunnerOwnershipFixture(t, "missing-admission")
}
func TestMacOwnershipMismatchedAdmission(t *testing.T) {
	testenv.MacRunnerOwnershipFixture(t, "mismatched-admission")
}
