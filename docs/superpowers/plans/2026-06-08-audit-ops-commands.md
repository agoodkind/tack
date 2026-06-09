# Audit Ops Commands (TACK-328) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every mutating ops command, plus read ops, record an audit event through the same recorder the API uses, identified by an explicit per-invocation operator flag.

**Architecture:** Every CLI command declares a static `audit.AuditSpec`. One choke-point in `clispec.cobraCommand`'s `RunE` resolves the operator from `--operator-*` flags, builds the Kafka recorder, preflights it, runs the command, and records one event. Global ops land on a reserved `SystemOrgID` chain; entity ops stamp the real org and delta through the `audit` context. tack-ops reaches Kafka through a loopback-published `HOST` listener.

**Tech Stack:** Go 1.26, `clispec`/cobra CLI, franz-go `v1.21.1` Kafka producer, pgx/YugabyteDB audit ledger, FoundationDB.

**Spec:** `docs/superpowers/specs/2026-06-08-audit-ops-commands-design.md`.

**Import-cycle constraint (binding):** `ops` imports `clispec`; `clispec` imports `cli`; `audit` imports none of them. So: the `OperatorIdentitySource` interface and `OperatorPrincipal` live in `audit`; `FlagOperatorSource` lives in `cli`; the choke-point lives in `clispec`. Nothing new is added to `ops/operator.go`.

**Branch:** `tack-328-audit-ops-commands` (already checked out).

**Commands:** build `make build`; unit `make test-unit`; integration `make test-integration`; format `make fmt`.

---

## Task 1: new `operator` actor kind

**Files:**
- Modify: `internal/audit/recorder.go:64-69`
- Modify: `internal/audit/yugabyte.go` (the `actorKindCode` function)
- Test: `internal/audit/yugabyte_test.go` (create if absent)

- [ ] **Step 1: Write the failing test**

```go
func TestActorKindCodeOperator(t *testing.T) {
	if got := actorKindCode(ActorOperator); got != 5 {
		t.Fatalf("actorKindCode(ActorOperator) = %d, want 5", got)
	}
}
```

- [ ] **Step 2: Run it, expect FAIL** (`ActorOperator` undefined)

Run: `go test ./internal/audit/ -run TestActorKindCodeOperator`
Expected: compile error, `undefined: ActorOperator`.

- [ ] **Step 3: Add the constant.** In `recorder.go`, add to the `const` block:

```go
	ActorSystem  ActorType = "system"
	ActorToken   ActorType = "api_token"
	ActorOperator ActorType = "operator"
```

- [ ] **Step 4: Add the mapping.** In `yugabyte.go` `actorKindCode`, add before `default`:

```go
	case ActorOperator:
		return 5
```

- [ ] **Step 5: Run it, expect PASS.** `go test ./internal/audit/ -run TestActorKindCodeOperator`

- [ ] **Step 6: Commit**

```bash
git add internal/audit/recorder.go internal/audit/yugabyte.go internal/audit/yugabyte_test.go
git commit -m "Add operator actor kind to audit recorder"
```

---

## Task 2: `AuditSpec` and `SystemOrgID`

**Files:**
- Create: `internal/audit/ops.go`
- Test: `internal/audit/ops_test.go`

- [ ] **Step 1: Write the failing test**

```go
package audit

import (
	"github.com/google/uuid"
	"testing"
)

func TestSystemOrgIDStableNonNil(t *testing.T) {
	if SystemOrgID == uuid.Nil {
		t.Fatal("SystemOrgID must be non-nil so Reader.Query can target it")
	}
	if SystemOrgID.String() != "00000000-0000-0000-0000-0000000005ee" {
		t.Fatalf("SystemOrgID changed: %s", SystemOrgID)
	}
}
```

- [ ] **Step 2: Run it, expect FAIL** (undefined `SystemOrgID`).

- [ ] **Step 3: Create `internal/audit/ops.go`**

```go
// Operator-command audit primitives shared by clispec (the dispatch
// choke-point) and ops (the command declarations). Kept in audit so neither
// importer creates a cycle.
package audit

import "github.com/google/uuid"

// SystemOrgID is the reserved, fixed, non-nil org that owns the chain for
// global ops with no real org (migrate, seed-roles, provision, deploy, backup,
// reindex, backfill). It is only a chain partition key; no product node uses
// it. Non-nil so audit.Reader.Query, which rejects uuid.Nil, can target it.
var SystemOrgID = uuid.MustParse("00000000-0000-0000-0000-0000000005ee")

// AuditSpec is the static audit declaration every CLI command carries. The
// zero value (empty Verb) means "do not record" and is used only by serve.
type AuditSpec struct {
	Verb            string
	Mutates         bool
	BootstrapExempt bool
	Reads           bool
}
```

- [ ] **Step 4: Run it, expect PASS.** `go test ./internal/audit/ -run TestSystemOrgIDStableNonNil`

- [ ] **Step 5: Commit**

```bash
git add internal/audit/ops.go internal/audit/ops_test.go
git commit -m "Add AuditSpec and SystemOrgID to audit package"
```

---

## Task 3: `ReachabilityChecker` and `Ping` implementations

**Files:**
- Modify: `internal/audit/ops.go` (add interface)
- Modify: `internal/audit/kafka_recorder.go`, `internal/audit/yugabyte.go`, and the `NoopRecorder` file (find with `go doc ./internal/audit NoopRecorder`)
- Test: `internal/audit/ops_test.go`

- [ ] **Step 1: Write the failing test** (Noop is the only one unit-testable without a broker/DB)

```go
func TestNoopRecorderPingNil(t *testing.T) {
	var rc ReachabilityChecker = NoopRecorder{}
	if err := rc.Ping(context.Background()); err != nil {
		t.Fatalf("NoopRecorder.Ping = %v, want nil", err)
	}
}
```

- [ ] **Step 2: Run it, expect FAIL** (NoopRecorder has no Ping; not a ReachabilityChecker).

- [ ] **Step 3: Add the interface** to `internal/audit/ops.go`:

```go
import "context"

// ReachabilityChecker is the optional preflight a recorder may implement so the
// ops choke-point can fail closed before a mutation when the ledger is down.
type ReachabilityChecker interface {
	Ping(ctx context.Context) error
}
```

- [ ] **Step 4: Implement Ping on each recorder.**

In `kafka_recorder.go`:

```go
// Ping probes broker reachability with a Metadata request so the ops
// choke-point can fail closed before a mutation. franz-go's Ping returns nil on
// the first reachable broker.
func (k *KafkaRecorder) Ping(ctx context.Context) error {
	return k.client.Ping(ctx)
}
```

In `yugabyte.go` (YBRecorder has `pool`):

```go
func (r *YBRecorder) Ping(ctx context.Context) error {
	return r.pool.Ping(ctx)
}
```

On `NoopRecorder`:

```go
func (NoopRecorder) Ping(context.Context) error { return nil }
```

- [ ] **Step 5: Run it, expect PASS.** `go test ./internal/audit/ -run TestNoopRecorderPingNil`

- [ ] **Step 6: Commit**

```bash
git add internal/audit/ops.go internal/audit/kafka_recorder.go internal/audit/yugabyte.go internal/audit/<noop file>
git commit -m "Add ReachabilityChecker Ping to audit recorders"
```

---

## Task 4: `NewRecorderFromConfig` (move selection into audit)

**Files:**
- Create: `internal/audit/from_config.go`
- Modify: `cmd/server/audit_runtime.go:78-110` (delegate to it)
- Test: `internal/audit/from_config_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestNewRecorderFromConfigSelectsByConfig(t *testing.T) {
	noop := NewRecorderFromConfig(context.Background(), &config.Config{})
	if _, ok := noop.(NoopRecorder); !ok {
		t.Fatalf("empty config = %T, want NoopRecorder", noop)
	}
	kafka := NewRecorderFromConfig(context.Background(),
		&config.Config{AuditKafkaBrokers: "[::1]:9094", AuditKafkaTopic: "audit.events.v1"})
	if _, ok := kafka.(*KafkaRecorder); !ok {
		t.Fatalf("brokers set = %T, want *KafkaRecorder", kafka)
	}
}
```

- [ ] **Step 2: Run it, expect FAIL** (undefined `NewRecorderFromConfig`).

- [ ] **Step 3: Create `internal/audit/from_config.go`** by moving the body of `cmd/server/audit_runtime.go`'s `buildAuditRecorder` verbatim, renamed and with the `config` import:

```go
package audit

import (
	"context"
	"log/slog"

	"goodkind.io/tack/internal/config"
)

// NewRecorderFromConfig selects the raw audit Recorder by configuration: Kafka
// when AUDIT_KAFKA_BROKERS is set, else the synchronous Yugabyte recorder when
// AUDIT_WRITER_DSN is set, else NoopRecorder. It does not wrap in
// SuppressingRecorder; callers add that. Setup failure degrades to noop.
func NewRecorderFromConfig(ctx context.Context, cfg *config.Config) Recorder {
	brokers := SplitBrokers(cfg.AuditKafkaBrokers)
	if len(brokers) > 0 {
		rec, err := NewKafkaRecorder(KafkaConfig{
			Brokers:        brokers,
			Topic:          cfg.AuditKafkaTopic,
			ClientID:       cfg.AuditKafkaClientID,
			ProduceTimeout: cfg.AuditKafkaProduceTimeout,
		})
		if err != nil {
			slog.ErrorContext(ctx, "audit.kafka_setup_failed", slog.String("err", err.Error()))
			return NoopRecorder{}
		}
		return rec
	}
	if cfg.AuditWriterDSN == "" {
		return NoopRecorder{}
	}
	yb, err := NewYBRecorder(ctx, cfg.AuditWriterDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit.writer_setup_failed", slog.String("err", err.Error()))
		return NoopRecorder{}
	}
	return yb
}
```

Verify no import cycle: `config` imports only `os`, `path/filepath`, `time`, `caarlos0/env`; it does not import `audit`.

- [ ] **Step 4: Rewire `cmd/server/audit_runtime.go`.** Replace the body of `buildAuditRecorder` with:

```go
func buildAuditRecorder(ctx context.Context, cfg *config.Config) audit.Recorder {
	rec := audit.NewRecorderFromConfig(ctx, cfg)
	switch rec.(type) {
	case audit.NoopRecorder:
		slog.WarnContext(ctx, "audit.writer_disabled",
			slog.String("reason", "AUDIT_KAFKA_BROKERS and AUDIT_WRITER_DSN unset; ledger writes are noop"))
	default:
		slog.InfoContext(ctx, "audit.recorder_enabled")
	}
	return rec
}
```

- [ ] **Step 5: Run tests, expect PASS.** `go test ./internal/audit/ -run TestNewRecorderFromConfig` and `make build`.

- [ ] **Step 6: Commit**

```bash
git add internal/audit/from_config.go internal/audit/from_config_test.go cmd/server/audit_runtime.go
git commit -m "Extract audit NewRecorderFromConfig and reuse in cmd/server"
```

---

## Task 5: ops-event context helpers and operator identity interface

**Files:**
- Modify: `internal/audit/ops.go` (interface + principal + context helpers)
- Modify: `internal/audit/context.go` (reuse the scope-builder holder)
- Test: `internal/audit/ops_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestOpsEventContextRoundTrip(t *testing.T) {
	ctx := WithScopeBuilder(context.Background())
	SetOpsEvent(ctx, Entity{Type: "node", ID: uuid.Max}, &Delta{Changed: []string{"x"}})
	ent, delta := OpsEventFromContext(ctx)
	if ent.Type != "node" || delta == nil || len(delta.Changed) != 1 {
		t.Fatalf("round trip lost data: %+v %+v", ent, delta)
	}
}
```

- [ ] **Step 2: Run it, expect FAIL** (undefined `SetOpsEvent`/`OpsEventFromContext`).

- [ ] **Step 3: Add to `internal/audit/ops.go`**

```go
// OperatorPrincipal is the resolved identity of whoever invoked an ops command.
type OperatorPrincipal struct {
	ID     uuid.UUID
	Email  string
	Name   string
	Source string // "flag" today; "sso" later
}

// OperatorIdentitySource resolves the operator for one invocation. The flag
// source lives in internal/cli; a future SSO source implements the same method.
type OperatorIdentitySource interface {
	Resolve(ctx context.Context) (OperatorPrincipal, error)
}
```

- [ ] **Step 4: Add the context helpers.** Inspect `internal/audit/context.go` for the scope-builder holder type, then add an entity+delta field to it plus:

```go
// SetOpsEvent stamps the entity and delta for an ops command onto the scope
// builder so the choke-point can read them after Run, mirroring SetScopeFields.
func SetOpsEvent(ctx context.Context, entity Entity, delta *Delta) {
	if b := scopeBuilderFrom(ctx); b != nil {
		b.opsEntity = entity
		b.opsDelta = delta
	}
}

// OpsEventFromContext returns what SetOpsEvent stored, zero values if unset.
func OpsEventFromContext(ctx context.Context) (Entity, *Delta) {
	if b := scopeBuilderFrom(ctx); b != nil {
		return b.opsEntity, b.opsDelta
	}
	return Entity{}, nil
}
```

(Use the actual holder accessor name from `context.go`; the builder is the same one `WithScopeBuilder`/`SetScopeFields` use.)

- [ ] **Step 5: Run it, expect PASS.** `go test ./internal/audit/ -run TestOpsEventContextRoundTrip`

- [ ] **Step 6: Commit**

```bash
git add internal/audit/ops.go internal/audit/context.go internal/audit/ops_test.go
git commit -m "Add operator identity interface and ops-event context helpers"
```

---

## Task 6: operator flags on `cli.Factory`

**Files:**
- Modify: `internal/cli/factory.go`
- Test: `internal/cli/factory_test.go` (create)

- [ ] **Step 1: Write the failing test**

```go
func TestFactoryOperatorFlags(t *testing.T) {
	f := &Factory{}
	root := &cobra.Command{Use: "tack", RunE: func(*cobra.Command, []string) error { return nil }}
	f.RegisterGlobalFlags(root)
	root.SetArgs([]string{"--operator-id", "11111111-1111-1111-1111-111111111111", "--operator-email", "a@b.c"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got, ok := f.Operator()
	if !ok || got.ID != "11111111-1111-1111-1111-111111111111" || got.Email != "a@b.c" {
		t.Fatalf("Operator() = %+v ok=%v", got, ok)
	}
}
```

- [ ] **Step 2: Run it, expect FAIL** (undefined `Operator`).

- [ ] **Step 3: Extend `Factory`** in `factory.go`:

```go
type Factory struct {
	Cfg *config.Config
	In  io.Reader
	Out io.Writer
	Err io.Writer

	output       *string
	operatorID    *string
	operatorEmail *string
	operatorName  *string
}

// OperatorFlags is the raw operator identity from the command line.
type OperatorFlags struct {
	ID    string
	Email string
	Name  string
}

// Operator returns the parsed --operator-* flags; ok is false until they are
// registered and parsed.
func (f *Factory) Operator() (OperatorFlags, bool) {
	if f.operatorID == nil {
		return OperatorFlags{}, false
	}
	return OperatorFlags{ID: *f.operatorID, Email: derefStr(f.operatorEmail), Name: derefStr(f.operatorName)}, true
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
```

Extend `RegisterGlobalFlags`:

```go
func (f *Factory) RegisterGlobalFlags(root *cobra.Command) {
	f.output = root.PersistentFlags().String("output", FormatText, "output format: text or json")
	f.operatorID = root.PersistentFlags().String("operator-id", "", "operator UUID recorded in the audit ledger (required for ops that read or mutate)")
	f.operatorEmail = root.PersistentFlags().String("operator-email", "", "operator email recorded in the audit ledger")
	f.operatorName = root.PersistentFlags().String("operator-name", "", "operator name recorded in the audit ledger")
}
```

(Add `System` initializer fields so exhaustruct stays satisfied: set the three new pointers to `nil`.)

- [ ] **Step 4: Run it, expect PASS.** `go test ./internal/cli/ -run TestFactoryOperatorFlags`

- [ ] **Step 5: Commit**

```bash
git add internal/cli/factory.go internal/cli/factory_test.go
git commit -m "Add operator identity persistent flags to cli Factory"
```

---

## Task 7: `FlagOperatorSource` in `cli`

**Files:**
- Create: `internal/cli/operator.go`
- Test: `internal/cli/operator_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestFlagOperatorSourceResolve(t *testing.T) {
	f := &Factory{operatorID: ptr("not-a-uuid")}
	if _, err := NewFlagOperatorSource(f).Resolve(context.Background()); err == nil {
		t.Fatal("bad id should error")
	}
	id := "11111111-1111-1111-1111-111111111111"
	f2 := &Factory{operatorID: ptr(id), operatorEmail: ptr("a@b.c")}
	p, err := NewFlagOperatorSource(f2).Resolve(context.Background())
	if err != nil || p.ID.String() != id || p.Source != "flag" {
		t.Fatalf("resolve = %+v err=%v", p, err)
	}
}
```

(Add a tiny `func ptr(s string) *string { return &s }` test helper.)

- [ ] **Step 2: Run it, expect FAIL** (undefined `NewFlagOperatorSource`).

- [ ] **Step 3: Create `internal/cli/operator.go`**

```go
package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
)

// FlagOperatorSource resolves the operator from the Factory's --operator-* flags.
// It implements audit.OperatorIdentitySource.
type FlagOperatorSource struct {
	factory *Factory
}

func NewFlagOperatorSource(f *Factory) FlagOperatorSource {
	return FlagOperatorSource{factory: f}
}

func (s FlagOperatorSource) Resolve(_ context.Context) (audit.OperatorPrincipal, error) {
	flags, ok := s.factory.Operator()
	if !ok || strings.TrimSpace(flags.ID) == "" {
		return audit.OperatorPrincipal{}, errors.New("operator identity required: pass --operator-id")
	}
	id, err := uuid.Parse(strings.TrimSpace(flags.ID))
	if err != nil {
		return audit.OperatorPrincipal{}, fmt.Errorf("operator id must be a UUID: %w", err)
	}
	return audit.OperatorPrincipal{ID: id, Email: flags.Email, Name: flags.Name, Source: "flag"}, nil
}
```

Verify `cli` importing `audit` is acyclic: `audit` does not import `cli`.

- [ ] **Step 4: Run it, expect PASS.** `go test ./internal/cli/ -run TestFlagOperatorSourceResolve`

- [ ] **Step 5: Commit**

```bash
git add internal/cli/operator.go internal/cli/operator_test.go
git commit -m "Add FlagOperatorSource implementing audit OperatorIdentitySource"
```

---

## Task 8: `Audit` field on `clispec.Operation`

**Files:**
- Modify: `internal/clispec/spec.go:60-72`

- [ ] **Step 1: Add the field** to `Operation[I]` (after `Run`):

```go
	New      func() I
	Run      func(ctx context.Context, in I, sink ResultSink) error
	Audit    audit.AuditSpec `exhaustruct:"optional"`
```

Add the import `"goodkind.io/tack/internal/audit"`. The `renderable` interface needs the spec visible to `cobraCommand`; add to the interface:

```go
type renderable interface {
	group() *Group
	auditSpec() audit.AuditSpec
	cobraCommand(f *cli.Factory) *cobra.Command
}

func (op Operation[I]) auditSpec() audit.AuditSpec { return op.Audit }
```

- [ ] **Step 2: Build, expect PASS.** `make build` (no behavior change yet).

- [ ] **Step 3: Commit**

```bash
git add internal/clispec/spec.go
git commit -m "Add Audit spec field to clispec Operation"
```

---

## Task 9: the dispatch choke-point in `clispec.cobraCommand`

**Files:**
- Create: `internal/clispec/audit.go` (the choke-point helper)
- Modify: `internal/clispec/cobra.go:83-92` (call it from `RunE`)
- Test: `internal/clispec/audit_test.go`

- [ ] **Step 1: Write the failing test** with a fake recorder and a fake source.

```go
func TestDispatchRecordsOnSuccess(t *testing.T) {
	rec := &captureRecorder{}
	src := fakeSource{p: audit.OperatorPrincipal{ID: uuid.Max, Source: "flag"}}
	err := runAudited(context.Background(), audit.AuditSpec{Verb: "ops.test", Mutates: true},
		src, func() audit.Recorder { return rec },
		func(ctx context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 || rec.events[0].Verb != "ops.test" ||
		rec.events[0].Actor.Type != audit.ActorOperator || rec.events[0].Context.OrgID != audit.SystemOrgID {
		t.Fatalf("event = %+v", rec.events)
	}
}

func TestDispatchAbortsMutationOnUnresolvedOperator(t *testing.T) {
	err := runAudited(context.Background(), audit.AuditSpec{Verb: "ops.test", Mutates: true},
		fakeSource{err: errors.New("no operator")}, func() audit.Recorder { return &captureRecorder{} },
		func(context.Context) error { t.Fatal("must not run"); return nil })
	if err == nil {
		t.Fatal("want abort on unresolved operator")
	}
}
```

(`captureRecorder` implements `audit.Recorder` + `audit.ReachabilityChecker` and is also a `*KafkaRecorder` stand-in: see step 3 note. `fakeSource` implements `audit.OperatorIdentitySource`.)

- [ ] **Step 2: Run it, expect FAIL** (undefined `runAudited`).

- [ ] **Step 3: Create `internal/clispec/audit.go`**

```go
package clispec

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

// runAudited is the single place every CLI command records to the ledger. It is
// generic over the recorder constructor so tests can inject a fake. requireKafka
// enforces the single-writer invariant for mutations: Noop or YB is rejected.
func runAudited(
	ctx context.Context,
	spec audit.AuditSpec,
	src audit.OperatorIdentitySource,
	newRecorder func() audit.Recorder,
	run func(ctx context.Context) error,
) error {
	if spec.Verb == "" { // serve and any unaudited command
		return run(ctx)
	}

	var actor audit.Actor
	if spec.Mutates || spec.Reads {
		p, err := src.Resolve(ctx)
		if err != nil {
			return fmt.Errorf("audited op %s: %w", spec.Verb, err)
		}
		actor = audit.Actor{Type: audit.ActorOperator, ID: p.ID, Email: p.Email, Name: p.Name}
	} else {
		actor = audit.Actor{Type: audit.ActorOperator}
	}

	rec := newRecorder()
	_, isKafka := rec.(*audit.KafkaRecorder)
	if spec.Mutates && !spec.BootstrapExempt && !isKafka {
		return fmt.Errorf("audited op %s: ledger not configured (need Kafka producer); refusing to mutate", spec.Verb)
	}
	if checker, ok := rec.(audit.ReachabilityChecker); ok {
		if err := checker.Ping(ctx); err != nil {
			if spec.Mutates && !spec.BootstrapExempt {
				return fmt.Errorf("audited op %s: ledger unreachable; refusing to mutate: %w", spec.Verb, err)
			}
			telemetry.L(ctx).WarnContext(ctx, "ops.audit.ledger_unreachable",
				telemetry.ErrAttr(err), telemetry.StrAttr("verb", spec.Verb))
		}
	}
	if closer, ok := rec.(interface{ Close() error }); ok {
		defer func() { _ = closer.Close() }()
	}

	ctx = audit.WithScopeBuilder(ctx)
	runErr := run(ctx)

	orgID := audit.ScopeFromContext(ctx).OrgID
	if orgID == uuid.Nil {
		orgID = audit.SystemOrgID
	}
	entity, delta := audit.OpsEventFromContext(ctx)
	outcome := audit.OutcomeOK
	var evErr *audit.EventError
	if runErr != nil {
		outcome = audit.OutcomeError
		evErr = &audit.EventError{Code: "error", Message: runErr.Error()}
	}
	ev := audit.Event{
		Verb:    spec.Verb,
		Actor:   actor,
		Entity:  entity,
		Context: audit.EventContext{OrgID: orgID, Source: audit.SourceSystem},
		Delta:   delta,
		Outcome: outcome,
		Error:   evErr,
	}
	rec = audit.SuppressingRecorder{Inner: rec} // stamps EventID + OccurredAt
	if recErr := rec.Record(ctx, ev); recErr != nil {
		if spec.Mutates && !spec.BootstrapExempt && runErr == nil {
			return fmt.Errorf("audited op %s completed but ledger record failed: %w", spec.Verb, recErr)
		}
		telemetry.L(ctx).ErrorContext(ctx, "ops.audit.record_failed",
			telemetry.ErrAttr(recErr), telemetry.StrAttr("verb", spec.Verb))
	}
	return runErr
}
```

(Use the real `telemetry` attr helpers; if their names differ, use `slog.String`/`slog.Any` via `telemetry.L(ctx)`.)

- [ ] **Step 4: Wire it into `cobraCommand`** (`cobra.go`), replacing the final `return op.Run(...)`:

```go
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		in := op.New()
		for i, arg := range op.Args {
			arg.bind(&in, args[i])
		}
		for _, apply := range applies {
			apply(&in)
		}
		return runAudited(cmd.Context(), op.Audit, cli.NewFlagOperatorSource(f),
			func() audit.Recorder { return audit.NewRecorderFromConfig(cmd.Context(), f.Cfg) },
			func(ctx context.Context) error { return op.Run(ctx, in, NewCLISink(f)) })
	}
```

Add imports `audit` and keep `cli`.

- [ ] **Step 5: Run tests, expect PASS.** `go test ./internal/clispec/ -run TestDispatch` and `make build`.

- [ ] **Step 6: Commit**

```bash
git add internal/clispec/audit.go internal/clispec/cobra.go internal/clispec/audit_test.go
git commit -m "Record every clispec command through one audit choke-point"
```

---

## Task 10: register operator flags at root

**Files:**
- Verify: `cmd/server/commands.go:42` already calls `f.RegisterGlobalFlags(root)`, which now registers the operator flags. No code change; add a smoke check.

- [ ] **Step 1:** Build and run `./server ops repair classes --help` mentally against the code: confirm `--operator-id` shows as a global flag because it is persistent on root. Run `make build`.

- [ ] **Step 2: Commit** (only if any wiring change was needed; otherwise skip).

---

## Task 11: fold the batch map into clispec

**Files:**
- Modify: `internal/ops/reindex.go`, `internal/ops/backfill_default_children.go` (re-declare as `clispec.Operation`)
- Modify: `internal/ops/ops.go` (drop the `registry`/`Register`/`Run`/`List`/`Get` map machinery once nothing uses it; keep `Env`/`NewEnv`)
- Modify: `internal/ops/cli.go` (remove `registerBatchOps`; register the two as leaf ops under `batchGroup`)

- [ ] **Step 1:** Convert `runReindex(ctx, env)` into a `reindexOp(f *cli.Factory) clispec.Operation[noInput]` that opens `NewEnv` inside `Run` (pattern from `repairPreviewOp`), with `Audit: audit.AuditSpec{Verb: "ops.reindex", Mutates: true}` and `Group: batchGroup`. Same for `backfillDefaultChildrenOp` with verb `ops.backfill.default_children`.

- [ ] **Step 2:** In `cli.go` `RegisterCommands`, replace `registerBatchOps(reg, f)` with `clispec.Register(reg, reindexOp(f))` and `clispec.Register(reg, backfillDefaultChildrenOp(f))`. Delete `registerBatchOps` and the `strings`/`List` usage.

- [ ] **Step 3:** In `ops.go`, delete `registry`, `Register`, `Get`, `List`, `Run`, `Operation`. Keep `Env`, `NewEnv`, `Close`. Remove now-unused imports (`sort`, otel span helpers if only `Run` used them).

- [ ] **Step 4:** `make build` and `make test-unit`, expect PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/ops/reindex.go internal/ops/backfill_default_children.go internal/ops/ops.go internal/ops/cli.go
git commit -m "Fold ops batch registry into the clispec registry"
```

---

## Task 12: declare audit specs on the ops commands

For each command, add an `Audit:` field to its `clispec.Operation` literal. Exact values:

| Command (file) | Verb | Mutates | BootstrapExempt | Reads |
| --- | --- | --- | --- | --- |
| `repairApplyOp` (cli_repair.go) | `ops.repair.apply` | true | false | false |
| `repairPreviewOp` (cli_repair.go) | `ops.repair.preview` | false | false | true |
| `repairClassesOp` (cli_repair.go) | `ops.repair.classes` | false | false | true |
| `auditSeedRolesOp` (cli_audit.go) | `ops.audit.seed_roles` | true | true | false |
| `provisionOp` (provision.go) | `ops.provision` | true | true | false |
| `inspectReadOp`/`Find`/`Query` (cli_inspect.go) | `ops.inspect.read`/`.find`/`.query` | false | false | true |
| `verifyNodeOp` (cli_verify.go) | `ops.verify.node` | false | false | true |
| `validateNodeOp` (cli_validate.go) | `ops.validate.node` | false | false | true |
| `reindexOp` (reindex.go) | `ops.reindex` | true | false | false |
| `backfillDefaultChildrenOp` | `ops.backfill.default_children` | true | false | false |
| `migrateOp` (commands.go) | `ops.migrate` | true | true | false |
| `seedOp` (seed.go) | `ops.seed` | true | true | false |

- [ ] **Step 1 (worked example, repair apply):** add `Audit: audit.AuditSpec{Verb: "ops.repair.apply", Mutates: true}` to the `Operation` literal in `repairApplyOp`. Remove the `--actor` param and the `ActorID` field; replace the actor source: after resolving the node and before `console.Apply`, resolve the org and stamp the event:

```go
resolve, err := env.Stores.Reader.Resolve(ctx, nodeID) // confirm the reader handle name on Env.Stores
if err != nil {
	return fmt.Errorf("repair apply: resolve node %s org: %w", nodeID, err)
}
audit.SetScopeFields(ctx, audit.Scope{OrgID: resolve.OrgID})
```

After a successful `console.Apply`, stamp entity + delta:

```go
audit.SetOpsEvent(ctx, audit.Entity{Type: "node", ID: nodeID},
	&audit.Delta{Changed: result.ChangedFields()}) // use whatever the apply result exposes; omit Delta if none
```

The operator is no longer a flag on this command; it comes from the global `--operator-id`. Update the `ApplyCommand` string in `repairPreviewOp` (cli_repair.go:124) to print `--operator-id <operator-uuid>` instead of `--actor <actor-uuid>`.

- [ ] **Step 2:** Add the `Audit:` literal to every other command per the table. For read ops no org stamping is needed (they default to `SystemOrgID`); add `audit.SetScopeFields` only where the op naturally resolves a node's org (inspect/verify/validate on a `--node`), otherwise leave global.

- [ ] **Step 3:** `make build`, `make fmt`, `make test-unit`. Expect PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/ops/ cmd/server/commands.go cmd/server/seed.go
git commit -m "Declare audit specs on all ops commands and drop repair --actor"
```

---

## Task 13: bootstrap exemption for provision and first-boot

**Files:**
- Modify: `internal/ops/provision.go`

- [ ] **Step 1:** `provisionOp` already has `Audit: ops.provision` (BootstrapExempt true) from Task 12, so its own steps run even with no ledger. The choke-point records one `ops.provision` event at the end. Because `migrate`/`seed-roles` run in-process inside `provisionRun` (not as nested clispec commands), they are covered by the single terminal `ops.provision` event; no per-substep recording is added.

- [ ] **Step 2:** Confirm `seedOp` and `migrateOp` are `BootstrapExempt: true` so that, run standalone before the ledger exists (first boot), they proceed; run on a live system they still record best-effort. `make build`.

- [ ] **Step 3: Commit** (if any change beyond Task 12).

```bash
git add internal/ops/provision.go
git commit -m "Keep provision and first-boot ops bootstrap-exempt for audit"
```

---

## Task 14: docker-compose Kafka HOST listener and tack-ops producer

**Files:**
- Modify: `docker-compose.yml` (kafka service env + new ports; tack-ops env)

- [ ] **Step 1:** In the `kafka` service `environment`, change the three listener lines to:

```yaml
      KAFKA_LISTENERS: PLAINTEXT://[::]:9092,CONTROLLER://[::]:9093,HOST://[::]:9094
      KAFKA_ADVERTISED_LISTENERS: PLAINTEXT://kafka:9092,HOST://[::1]:9094
      KAFKA_LISTENER_SECURITY_PROTOCOL_MAP: CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT,HOST:PLAINTEXT
```

- [ ] **Step 2:** Add a `ports` block to the `kafka` service (before `volumes:`):

```yaml
    ports:
      - "[::1]:9094:9094"
```

- [ ] **Step 3:** Add to the `tack-ops` service `environment`:

```yaml
      AUDIT_KAFKA_BROKERS: ${TACK_OPS_AUDIT_KAFKA_BROKERS:-[::1]:9094}
      AUDIT_KAFKA_TOPIC: ${AUDIT_KAFKA_TOPIC:-audit.events.v1}
      AUDIT_KAFKA_CLIENT_ID: ${AUDIT_KAFKA_CLIENT_ID:-tack-ops-audit-producer}
      AUDIT_KAFKA_PRODUCE_TIMEOUT: ${AUDIT_KAFKA_PRODUCE_TIMEOUT:-10s}
```

- [ ] **Step 4:** Validate: `docker compose --profile audit --profile ops config >/dev/null` (parses the file; do not bring the stack up locally).

- [ ] **Step 5: Commit**

```bash
git add docker-compose.yml
git commit -m "Add loopback Kafka HOST listener and tack-ops audit producer env"
```

---

## Task 15: configs repo paired change (separate repo, do not auto-apply)

**Files (in `~/Sites/configs`, a separate repo and PR):**
- `tack/tack.env.j2`: add `TACK_OPS_AUDIT_KAFKA_BROKERS={{ tack_ops_audit_kafka_brokers }}`
- `ansible/inventory/group_vars/tack_all.yml`: add `tack_ops_audit_kafka_brokers: "[::1]:9094"`

- [ ] **Step 1:** This is owned by the configs repo per the seam. Open it as a separate branch/PR there; it is not part of the tack-repo commit. No operator variables are added (identity is a runtime flag). Leave a note in the tack PR description linking the configs PR.

---

## Task 16: integration test (produce path via kfake)

**Files:**
- Test: `internal/test/integration/audit_ops_test.go` (gated by `TACK_INTEGRATION`)

- [ ] **Step 1: Write the test.** Stand up a `kfake` broker (franz-go `pkg/kfake`, already in go.mod), point a `KafkaRecorder` at it, drive `runAudited` with `AuditSpec{Verb: "ops.reindex", Mutates: true}` and a resolved operator, then consume the produced record and assert the decoded `audit.Event` has `Verb=="ops.reindex"`, `Actor.Type==audit.ActorOperator`, and `Context.OrgID==audit.SystemOrgID`.

```go
//go:build integration
// assert the produced Event, not a projected row: the test stack has no
// audit-consumer, so consumer projection (actor_kind=5 in audit.events) is
// covered by the consumer's own tests and the QA manual step.
```

- [ ] **Step 2:** Run: `make test-integration`. Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add internal/test/integration/audit_ops_test.go
git commit -m "Add kfake integration test for ops audit produce path"
```

---

## Task 17: full build and gates

- [ ] **Step 1:** `make build` (vet, golangci, staticcheck-extra, govulncheck). Expected: clean.
- [ ] **Step 2:** `make check`. Expected: clean.
- [ ] **Step 3:** `make test-unit`. Expected: PASS.
- [ ] **Step 4:** `make test-integration`. Expected: PASS.
- [ ] **Step 5:** QA manual (operator, never prod, never first): with the audit profile up and the HOST listener live, run `docker compose run --rm tack-ops ops audit seed-roles --operator-id <uuid> --operator-email you@x` and `docker compose exec app /server ops repair apply --operator-id <uuid> --node <uuid> --confirm <token> --yes`, then query the ledger via the audit MCP tools and confirm `actor_kind=5`, the right verbs, the system vs real org, and chain continuity. Run a mutation with the broker down and confirm it aborts; run an `inspect` with the broker down and confirm it proceeds.

---

## Self-review notes (gaps to watch during execution)

- **`Env.Stores` reader handle name** (Task 12) is unconfirmed; check `internal/adapters/foundationdb` `Stores` for the `NodeReader` field before writing the `Resolve` call.
- **`telemetry` attr helper names** (Task 9) may differ; fall back to `telemetry.L(ctx).WarnContext(ctx, msg, slog.String(...))`.
- **repair apply delta source** (Task 12): use whatever the apply `result` exposes; if it exposes no structured change set, omit `Delta` and stamp only the entity.
- **`NoopRecorder` file location** (Task 3): find via `go doc ./internal/audit NoopRecorder`.
- **Scope-builder field names** (Task 5): match the existing holder in `context.go` exactly.
