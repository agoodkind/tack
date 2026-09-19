package main

import (
	"context"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

// TestMain removes the test engines this binary started once every test has
// run; the binary exits with the tests' result.
func TestMain(m *testing.M) {
	m.Run()
	_ = testenv.Release(context.Background())
}
