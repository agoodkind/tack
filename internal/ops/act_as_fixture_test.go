package ops

import (
	"context"

	"goodkind.io/tack/internal/audit"
)

// capturedOutbox keeps every event a command records. A test reads the
// recorded ledger row instead of the command's report.
type capturedOutbox struct {
	events []audit.Event
}

func (o *capturedOutbox) WriteOutbox(_ context.Context, event audit.Event) error {
	o.events = append(o.events, event)
	return nil
}

type fixedOperator struct {
	principal audit.OperatorPrincipal
}

func (f fixedOperator) Resolve(context.Context) (audit.OperatorPrincipal, error) {
	return f.principal, nil
}
