package foundationdb

import (
	"context"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

// TestMain removes the test engines this binary started after every test
// runs. The binary exits with the tests' result.
func TestMain(m *testing.M) {
	m.Run()
	_ = testenv.Release(context.Background())
}
