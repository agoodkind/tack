package integration

import (
	"context"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

// TestMain starts the Mailpit server and trusts its certificate authority
// when the run selects a test that delivers mail, and removes the test
// engines this binary started once every test has run; the binary exits with
// the tests' result.
func TestMain(m *testing.M) {
	testenv.TrustMailpit(context.Background(), breakGlassMailTests)
	m.Run()
	_ = testenv.Release(context.Background())
}
