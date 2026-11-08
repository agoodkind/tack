package integration_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/datagen"
)

func expectedDatagenReportTargets(t *testing.T, fixture datagenReportFixture, row audit.Row, extra datagenReportExtra, payload []byte, seed int64) []audit.Target {
	t.Helper()
	switch extra.Input.Report {
	case "ops.audit.consumer_offsets":
		if row.Context.Target.Reference != "audit/consumer-offsets" || extra.Input.Topic != fixture.config.AuditKafkaTopic {
			t.Fatalf("consumer offset selection = %+v, target = %+v", extra.Input, row.Context.Target)
		}
		return datagenOffsetTargets(t, payload)
	case "ops.qa.datagen.commit_order":
		orgIDs := datagenReportOrgIDs(t, fixture, seed)
		var report struct {
			ReadAt time.Time `json:"read_at"`
		}
		if err := json.Unmarshal(payload, &report); err != nil {
			t.Fatalf("decode the commit-order time: %v", err)
		}
		if !slices.Equal(extra.Input.OrgIDs, orgIDs) || extra.Input.LimitPerOrg != 200 || extra.Input.EntityKind != "node" ||
			!extra.Input.Oldest.Equal(report.ReadAt.Add(-24*time.Hour)) || !extra.Input.Latest.Equal(report.ReadAt.Add(time.Minute)) ||
			row.Context.Target.Reference != "audit/datagen/commit-order" {
			t.Fatalf("commit-order aggregate selection = %+v", extra.Input)
		}
		return []audit.Target{externalReference("audit/datagen/commit-order")}
	case "ops.qa.datagen.manifest_verification":
		var report struct {
			OrgID     uuid.UUID `json:"org_id"`
			EventID   uuid.UUID `json:"event_id"`
			RequestID string    `json:"request_id"`
		}
		if err := json.Unmarshal(payload, &report); err != nil {
			t.Fatalf("decode the returned manifest identity: %v", err)
		}
		if !slices.Equal(extra.Input.OrgIDs, []uuid.UUID{report.OrgID}) || extra.Input.RequestID != report.RequestID ||
			row.Context.Target.URI != audit.AuditOrgURIPrefix+report.OrgID.String() || extra.Input.Wait != "2m0s" ||
			extra.Input.Poll != "1s" || extra.Input.Oldest.IsZero() || !extra.Input.Latest.After(extra.Input.Oldest) {
			t.Fatalf("manifest selection = %+v, returned identity = %+v", extra.Input, report)
		}
		return []audit.Target{audit.EventTarget(report.EventID)}
	default:
		return expectedDatagenVerbTargets(t, row, extra, payload)
	}
}

func datagenOffsetTargets(t *testing.T, payload []byte) []audit.Target {
	t.Helper()
	var report struct {
		Topic      string `json:"topic"`
		Partitions []struct {
			Group     string `json:"consumer_group"`
			Partition int32  `json:"partition"`
		} `json:"partitions"`
	}
	if err := json.Unmarshal(payload, &report); err != nil {
		t.Fatalf("decode returned offset aggregates: %v", err)
	}
	var targets []audit.Target
	for _, partition := range report.Partitions {
		reference := fmt.Sprintf("audit/consumer-offsets/%s/%s/%d", url.PathEscape(report.Topic), url.PathEscape(partition.Group), partition.Partition)
		targets = append(targets, externalReference(reference))
	}
	return append(targets, externalReference("audit/operator-outbox"), externalReference("audit/foundationdb-outbox"))
}

func datagenReportOrgIDs(t *testing.T, fixture datagenReportFixture, seed int64) []uuid.UUID {
	t.Helper()
	scale, err := datagen.ParseScale("small")
	if err != nil {
		t.Fatalf("parse generated-data test scale: %v", err)
	}
	var orgIDs []uuid.UUID
	for _, workspace := range datagen.PlanIdentities(fixture.config, seed, scale).Workspaces {
		if !slices.Contains(orgIDs, workspace.OrgID) {
			orgIDs = append(orgIDs, workspace.OrgID)
		}
	}
	return orgIDs
}

func requireDatagenCommittedChanges(t *testing.T, fixture datagenReportFixture, seed int64) {
	t.Helper()
	orgID := datagenReportOrgIDs(t, fixture, seed)[0]
	var rows []audit.Row
	waitFor(t, "committed generated node evidence", func() bool {
		var err error
		rows, err = fixture.pipeline.reader.Query(t.Context(), audit.QueryFilter{
			OrgID: orgID, Oldest: fixture.ledger.since, Latest: time.Now().Add(time.Hour),
			Action: string(audit.VerbNodeCreate), ActorID: uuid.Nil, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: 100,
		})
		if err != nil {
			t.Fatalf("read committed generated node evidence: %v", err)
		}
		return len(rows) > 0
	})
	stores, err := fdbadapter.NewStores(fixture.config.FDBClusterFile, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("open generated product records: %v", err)
	}
	for _, row := range rows {
		if row.Context.Phase != audit.PhaseChange {
			continue
		}
		if row.Context.OperationClass != audit.OperationDatabaseChange || row.Outcome != audit.OutcomeCompleted || row.Delta == nil {
			t.Fatalf("committed mutation lost its actual outcome: %+v", row)
		}
		view, err := stores.Views.Get(t.Context(), row.EntityID)
		if err != nil {
			t.Fatalf("read generated node %s: %v", row.EntityID, err)
		}
		if view != nil && view.OrgID == orgID {
			t.Logf("committed node=%s event=%s outcome=%s", row.EntityID, row.EventID, row.Outcome)
			return
		}
	}
	t.Fatal("the permanent mutation events did not identify a committed generated node")
}
