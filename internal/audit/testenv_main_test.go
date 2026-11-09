package audit

import (
	"context"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

// TestMain removes the test engines that this binary started after all tests
// finish. The test harness exits with the tests' result.
func TestMain(m *testing.M) {
	m.Run()
	_ = testenv.Release(context.Background())
}
