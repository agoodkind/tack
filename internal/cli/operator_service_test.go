package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
)

func TestServiceOperatorSourceResolvesServicePrincipal(t *testing.T) {
	factory := &Factory{operatorService: stringPointer("tack-app")}

	principal, err := (ServiceOperatorSource{Factory: factory}).Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if principal.Kind != audit.ActorService {
		t.Fatalf("kind = %q, want %q", principal.Kind, audit.ActorService)
	}
	if principal.Source != "service" {
		t.Fatalf("source = %q, want service", principal.Source)
	}
	if principal.Name != "tack-app" {
		t.Fatalf("name = %q, want tack-app", principal.Name)
	}
	if principal.Email != "" {
		t.Fatalf("email = %q, want empty; a service has no mailbox", principal.Email)
	}

	again, err := (ServiceOperatorSource{Factory: factory}).Resolve(t.Context())
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if again.ID != principal.ID {
		t.Fatalf("same service resolved to %s then %s, want a stable id", principal.ID, again.ID)
	}

	other := &Factory{operatorService: stringPointer("tack-worker")}
	otherPrincipal, err := (ServiceOperatorSource{Factory: other}).Resolve(t.Context())
	if err != nil {
		t.Fatalf("other Resolve: %v", err)
	}
	if otherPrincipal.ID == principal.ID {
		t.Fatalf("different services share id %s", principal.ID)
	}
}

func TestServiceOperatorSourceRejectsEmptyName(t *testing.T) {
	factory := &Factory{operatorService: stringPointer("   ")}

	_, err := (ServiceOperatorSource{Factory: factory}).Resolve(t.Context())
	if err == nil || !strings.Contains(err.Error(), "operator-service") {
		t.Fatalf("Resolve error = %v, want operator-service error", err)
	}
}

func TestNewOperatorSourcePrefersServiceFlag(t *testing.T) {
	factory := &Factory{Cfg: nil, In: nil, Out: nil, Err: nil}
	root := &cobra.Command{Use: "tack"}
	factory.RegisterGlobalFlags(root)
	if err := root.ParseFlags([]string{"--operator-service", "tack-app", "--execute"}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	principal, err := NewOperatorSource(factory).Resolve(t.Context())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if principal.Source != "service" || principal.Kind != audit.ActorService {
		t.Fatalf("principal = %+v, want the service source", principal)
	}
}

// TestNewOperatorSourceRefusesServiceAndOperatorID pins the conflict rule: the
// two flags name different actors, and picking one silently would attribute
// the action to the wrong identity.
func TestNewOperatorSourceRefusesServiceAndOperatorID(t *testing.T) {
	factory := &Factory{Cfg: nil, In: nil, Out: nil, Err: nil}
	root := &cobra.Command{Use: "tack"}
	factory.RegisterGlobalFlags(root)
	if err := root.ParseFlags([]string{
		"--operator-service", "tack-app",
		"--operator-id", "019dd222-440e-729a-a442-281aaf73ca30",
		"--operator-email", "ops@goodkind.io",
	}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}

	_, err := NewOperatorSource(factory).Resolve(t.Context())
	if err == nil {
		t.Fatal("ambiguous identity accepted, want it refused")
	}
	if !strings.Contains(err.Error(), "--operator-service") || !strings.Contains(err.Error(), "--operator-id") {
		t.Fatalf("error = %v, want it to name both flags", err)
	}
}
