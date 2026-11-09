package main

import (
	"context"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

// TestMain removes the test engines this binary started after every test has
// run. The binary exits with the test result.
func TestMain(m *testing.M) {
	m.Run()
	_ = testenv.Release(context.Background())
}
