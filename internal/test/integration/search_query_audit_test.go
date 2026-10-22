package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
)

// searchAuditPageSize limits the number of FoundationDB outbox entries that one read returns.
const searchAuditPageSize = 1000

// harnessOutboxTimeout limits the duration of each FoundationDB transaction for the harness outbox reader.
const harnessOutboxTimeout = 5 * time.Second

func harnessOutbox(t *testing.T, clusterFile string) *fdbadapter.OpsOutboxStore {
	t.Helper()
	stores, err := fdbadapter.NewStores(clusterFile, harnessOutboxTimeout, nil)
	if err != nil {
		t.Fatalf("the test could not open the FoundationDB outbox: %v", err)
	}
	return stores.OpsOutbox
}

// requireSearchAuditInvocation waits until the FoundationDB outbox contains
// exactly one completed node.search read event for requestID in the harness
// organization.
func requireSearchAuditInvocation(parent context.Context, harness *MCPHarness, requestID string) error {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := harnessReadEvents(ctx, harness, requestID)
		if err != nil {
			return err
		}
		if len(events) > 1 {
			return fmt.Errorf("expected one read event for search request %s, got %d", requestID, len(events))
		}
		if len(events) == 1 {
			event := events[0]
			if event.Verb != string(audit.VerbNodeSearch) || event.Outcome != audit.OutcomeCompleted ||
				event.Context.OrgID != harness.orgID {
				return fmt.Errorf("search request %s has read event %s %s in organization %s; expected %s %s in organization %s",
					requestID, event.Verb, event.Outcome, event.Context.OrgID,
					audit.VerbNodeSearch, audit.OutcomeCompleted, harness.orgID)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for the read event for search request %s: %w", requestID, ctx.Err())
		case <-ticker.C:
		}
	}
}

// harnessReadEvents returns read events with requestID from the FoundationDB outbox.
// It joins the parts of each multipart event before decoding the event.
func harnessReadEvents(ctx context.Context, harness *MCPHarness, requestID string) ([]audit.Event, error) {
	type outboxRecord struct {
		Message string `json:"audit_message"`
		EventID string `json:"event_id"`
		Payload []byte `json:"payload"`
	}
	if harness.outbox == nil {
		return nil, fmt.Errorf("test harness for request %s does not have a FoundationDB outbox reader", requestID)
	}
	envelopes := map[string][]byte{}
	var events []audit.Event
	var mark []byte
	for {
		entries, err := harness.outbox.ReadOutboxFrom(ctx, mark, searchAuditPageSize)
		if err != nil {
			return nil, fmt.Errorf("read the FoundationDB outbox: %w", err)
		}
		for _, entry := range entries {
			var record outboxRecord
			if json.Unmarshal(entry.Event, &record) != nil {
				continue
			}
			var event audit.Event
			switch record.Message {
			case "":
				if json.Unmarshal(entry.Event, &event) != nil {
					continue
				}
			case "event_part":
				envelopes[record.EventID] = append(envelopes[record.EventID], record.Payload...)
				continue
			default:
				var envelope struct {
					Event audit.Event `json:"event"`
				}
				if json.Unmarshal(envelopes[record.EventID], &envelope) != nil {
					continue
				}
				event = envelope.Event
			}
			if event.Context.RequestID == requestID && event.Context.OperationClass == audit.OperationRead {
				events = append(events, event)
			}
		}
		if len(entries) < searchAuditPageSize {
			return events, nil
		}
		mark = entries[len(entries)-1].Mark
	}
}
