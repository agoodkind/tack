package cli

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// serviceNamespace defines the version 5 UUID namespace for service names.
// The same name produces the same actor ID without a registry. Service names
// and operator emails use different namespaces to separate their identity
// derivations, including when the name matches an email. Changing this
// namespace changes every derived service ID. Keep it fixed for the lifetime
// of the deployment.
var serviceNamespace = uuid.MustParse("0a6f7572-cafe-dead-beef-000000000005")

// ServiceActorID derives the ledger actor ID for a service name.
//
// The service identity source and readers that select events by service name
// use this derivation. The function uses the supplied name without trimming
// whitespace.
//
//	first := ServiceActorID("tack-app")
//	again := ServiceActorID("tack-app")
//	same := first == again // true
func ServiceActorID(serviceName string) uuid.UUID {
	return uuid.NewSHA1(serviceNamespace, []byte(serviceName))
}

// ServiceOperatorSource resolves a service identity from --operator-service.
// A daemon such as serve must use a service actor rather than the identity of
// the human who deployed it.
type ServiceOperatorSource struct {
	// Factory provides the parsed global operator flags.
	Factory *Factory
}

// Resolve returns the service principal selected by --operator-service.
//
// The factory must supply a nonempty service name after whitespace trimming.
// The principal uses that name, a stable service ID, and a trimmed session ID;
// it does not supply an email or accountable operator.
//
//	source := ServiceOperatorSource{Factory: factory}
//	principal, err := source.Resolve(ctx)
func (s ServiceOperatorSource) Resolve(ctx context.Context) (audit.OperatorPrincipal, error) {
	if s.Factory == nil {
		err := errors.New("operator service source has no factory")
		slog.ErrorContext(ctx, "operator.service.factory_missing", slog.String("err", err.Error()))
		return audit.OperatorPrincipal{}, err
	}
	serviceName := strings.TrimSpace(s.Factory.OperatorService())
	if serviceName == "" {
		err := errors.New("operator-service is required for service identity")
		slog.ErrorContext(ctx, "operator.service.name_missing", slog.String("err", err.Error()))
		return audit.OperatorPrincipal{}, err
	}
	return audit.OperatorPrincipal{
		ID:         ServiceActorID(serviceName),
		Email:      "",
		Name:       serviceName,
		Source:     "service",
		Kind:       audit.ActorService,
		SessionID:  strings.TrimSpace(s.Factory.OperatorSession()),
		OnBehalfOf: nil,
	}, nil
}
