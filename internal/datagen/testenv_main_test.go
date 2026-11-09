package datagen

import (
	"context"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

// TestMain runs every test, then asks testenv.Release to remove the engines
// this binary started. The binary exits with the tests' result.
func TestMain(m *testing.M) {
	m.Run()
	_ = testenv.Release(context.Background())
}
