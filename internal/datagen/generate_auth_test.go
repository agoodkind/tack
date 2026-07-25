package datagen

import (
	"net/http"
	"strings"
	"testing"

	"goodkind.io/tack/internal/runtime"
)

func TestRejectedTokenProbesRequireHTTPUnauthorizedInSortedOrder(t *testing.T) {
	t.Parallel()
	var authorizations []string
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorizations = append(authorizations, request.Header.Get("Authorization"))
		writer.WriteHeader(http.StatusUnauthorized)
	})
	generator := &Generator{
		driver:         NewDriver(testGraph(handler), false, 245),
		identities:     Identities{ExpiredToken: "expired", BogusToken: "bogus"},
		productionAuth: true,
	}
	if err := generator.exerciseRejectedTokens(t.Context()); err != nil {
		t.Fatalf("exerciseRejectedTokens() error = %v", err)
	}
	got := strings.Join(authorizations, ",")
	if got != "Bearer bogus,Bearer expired" {
		t.Fatalf("Authorization order = %q", got)
	}
}

func TestRejectedTokenProbesRejectNonAuthenticationErrors(t *testing.T) {
	t.Parallel()
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	})
	generator := &Generator{
		driver:         NewDriver(testGraph(handler), false, 245),
		identities:     Identities{ExpiredToken: "expired", BogusToken: "bogus"},
		productionAuth: true,
	}
	err := generator.exerciseRejectedTokens(t.Context())
	if err == nil || !strings.Contains(err.Error(), "expected HTTP 401") {
		t.Fatalf("exerciseRejectedTokens() error = %v", err)
	}
}

func TestRejectedTokenProbesSkipDevelopmentAuth(t *testing.T) {
	t.Parallel()
	generator := &Generator{
		driver:         NewDriver(nil, false, 245),
		productionAuth: false,
	}
	if err := generator.exerciseRejectedTokens(t.Context()); err != nil {
		t.Fatalf("exerciseRejectedTokens() error = %v", err)
	}
}

func testGraph(handler http.Handler) *runtime.Graph {
	return &runtime.Graph{
		MCPHandler: handler,
		AuthMiddleware: func(next http.Handler) http.Handler {
			return next
		},
	}
}
