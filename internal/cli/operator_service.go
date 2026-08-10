package cli

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// serviceNamespace seeds the version 5 UUID derived from a service name, so
// the same service gets the same actor id with no registry to maintain. It is
// distinct from the operator email namespace so a service named like an email
// can never collide with a human actor id. Changing it renames every service
// in the ledger, so it is fixed for the life of the deployment.
var serviceNamespace = uuid.MustParse("0a6f7572-cafe-dead-beef-000000000005")

// ServiceOperatorSource resolves a non-human identity from the service name
// flag. A daemon such as serve runs as a service actor, never as the human who
// happened to deploy it.
type ServiceOperatorSource struct {
	// Factory provides the parsed global operator flags.
	Factory *Factory
}

// Resolve returns the service principal named by the service flag.
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
		ID:     uuid.NewSHA1(serviceNamespace, []byte(serviceName)),
		Email:  "",
		Name:   serviceName,
		Source: "service",
		Kind:   audit.ActorService,
	}, nil
}
