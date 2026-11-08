package integration_test

import (
	"encoding/json"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/response"
)

type datagenReportExtra struct {
	commandExtra
	DeployCommit string `json:"deploy_commit"`
	Input        struct {
		Report      string      `json:"report"`
		OrgIDs      []uuid.UUID `json:"org_ids"`
		Verbs       []string    `json:"verbs"`
		Topic       string      `json:"topic"`
		RequestID   string      `json:"request_id"`
		Oldest      time.Time   `json:"oldest"`
		Latest      time.Time   `json:"latest"`
		LimitPerOrg int         `json:"limit_per_org"`
		EntityKind  string      `json:"aggregate_entity_kind"`
		Wait        string      `json:"wait"`
		Poll        string      `json:"poll"`
	} `json:"input"`
}

func serviceDatagenActor(operatorID uuid.UUID) uuid.UUID {
	return cli.ServiceActorID(searchReadService(operatorID))
}

func requireDatagenReportEvidence(
	t *testing.T, fixture datagenReportFixture, row audit.Row, extra datagenReportExtra,
	payload []byte, operatorID, parent uuid.UUID, metadata response.Metadata, seed int64,
) {
	t.Helper()
	eventContext := row.Context
	if row.ActorID != serviceDatagenActor(operatorID) || eventContext.ParentOperationID != parent ||
		eventContext.OperationID == parent || eventContext.OperationID != extra.OpID ||
		eventContext.RequestID != metadata.RequestID || eventContext.TraceID != metadata.TraceID ||
		eventContext.RequestID == "" || eventContext.TraceID == "" || eventContext.Target == nil ||
		eventContext.StartedAt.IsZero() || eventContext.CompletedAt.Before(eventContext.StartedAt) ||
		!row.EventTime.Equal(eventContext.StartedAt.Truncate(time.Microsecond)) || extra.DeployCommit != searchReadDeployCommit ||
		extra.SessionID != searchReadSession || extra.OnBehalfOf == nil || extra.OnBehalfOf.OperatorID != operatorID {
		t.Fatalf("report %s lost its actor, parent, correlation, or original times: %+v, extra=%+v", extra.Input.Report, row, extra)
	}
	verb := audit.VerbAuditRead
	if extra.Input.Report == "ops.audit.consumer_offsets" {
		verb = audit.VerbOpsAuditConsumerOffsets
	}
	requireReadRow(t, row, verb, audit.OutcomeCompleted, "", payload)
	targets := expectedDatagenReportTargets(t, fixture, row, extra, payload, seed)
	digest, parts, err := audit.ReadManifestDigest(targets)
	if err != nil {
		t.Fatalf("digest report identities: %v", err)
	}
	evidence := eventContext.Read
	if evidence.Count != uint64(len(targets)) || evidence.ManifestSHA256 != digest || evidence.ManifestParts != parts ||
		evidence.PayloadFormat != "application/json" || !slices.Equal(fixture.ledger.manifestTargets(t, row), targets) {
		t.Fatalf("report %s evidence differs from returned aggregates: %+v, want %+v", extra.Input.Report, evidence, targets)
	}
	t.Logf("permanent report=%s event=%s parent=%s operation=%s outcome=%s count=%d sha256=%s",
		extra.Input.Report, row.EventID, parent, eventContext.OperationID, row.Outcome, evidence.Count, evidence.PayloadSHA256)
}

func requireDatagenMutationOutcome(t *testing.T, rows []audit.Row, outcome audit.Outcome, verb audit.Verb) uuid.UUID {
	t.Helper()
	var parent uuid.UUID
	intents, outcomes := 0, 0
	for _, row := range rows {
		if row.Action != string(verb) {
			continue
		}
		var extra commandExtra
		if err := json.Unmarshal(row.Extra, &extra); err != nil || extra.OpID == uuid.Nil {
			t.Fatalf("decode the initiating command operation: %v", err)
		}
		if parent != uuid.Nil && parent != extra.OpID {
			t.Fatalf("the seed intent and outcome use different operations: %s and %s", parent, extra.OpID)
		}
		parent = extra.OpID
		if row.Outcome == audit.OutcomePending {
			intents++
		}
		if row.Outcome == outcome {
			outcomes++
			if outcome == audit.OutcomeError && (row.Error == nil || row.Error.Code != "command_failed") {
				t.Fatalf("the seed command did not record its actual report failure: %+v", row)
			}
		}
	}
	if intents != 1 || outcomes != 1 {
		t.Fatalf("seed command intent/outcome counts = %d/%d, want 1/1: %s", intents, outcomes, describeDatagenRows(rows))
	}
	return parent
}

func expectedDatagenVerbTargets(t *testing.T, row audit.Row, extra datagenReportExtra, payload []byte) []audit.Target {
	t.Helper()
	if len(extra.Input.OrgIDs) != 1 || row.Context.Target.URI != audit.AuditOrgURIPrefix+extra.Input.OrgIDs[0].String() {
		t.Fatalf("verb report target does not select its organization: %+v", extra.Input)
	}
	var report struct {
		Verbs []struct {
			Verb string `json:"verb"`
		} `json:"verbs"`
	}
	if err := json.Unmarshal(payload, &report); err != nil {
		t.Fatalf("decode returned verb aggregates: %v", err)
	}
	var verbs []string
	var targets []audit.Target
	for _, entry := range report.Verbs {
		verbs = append(verbs, entry.Verb)
		uri := audit.AuditOrgURIPrefix + extra.Input.OrgIDs[0].String() + "/verb/" + url.PathEscape(entry.Verb)
		targets = append(targets, audit.ResourceTarget(uri))
	}
	if !slices.Equal(verbs, extra.Input.Verbs) {
		t.Fatalf("returned aggregate verbs %v differ from selection %v", verbs, extra.Input.Verbs)
	}
	return targets
}
