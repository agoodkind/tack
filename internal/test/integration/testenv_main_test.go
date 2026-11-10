package integration

import (
	"context"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

// Outside short mode, TestMain starts Mailpit and trusts its certificate
// authority when the selected tests deliver mail. It removes the engines this
// binary started after all tests run. The binary exits with the test result.
func TestMain(m *testing.M) {
	testenv.TrustMailpit(context.Background(), breakGlassMailTests)
	m.Run()
	_ = testenv.Release(context.Background())
}
