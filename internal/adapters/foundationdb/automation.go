//go:build fdb

package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"

	"goodkind.io/tack/internal/domain/node"
	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

// AutomationStore implements node.AutomationRepository using FoundationDB.
//
// Primary key:
//
//	automation_rule, orgID, ruleID → AutomationRule (JSON)
//
// Trigger index:
//
//	automation_rule_by_trigger, orgID, workspaceID, nodeType, trigger, ruleID → nil
type AutomationStore struct {
	db fdb.Database
}

func NewAutomationStore(db fdb.Database) *AutomationStore {
	return &AutomationStore{db: db}
}

func (s *AutomationStore) Set(_ context.Context, rule *node.AutomationRule) error {
	b, err := json.Marshal(rule)
	if err != nil {
		return fmt.Errorf("marshal automation rule: %w", err)
	}
	primaryKey := automationRuleKey(rule.OrgID, rule.ID)
	indexKey := automationTriggerIndexKey(rule.OrgID, rule.WorkspaceID, rule.NodeType, rule.Trigger, rule.ID)
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(fdb.Key(primaryKey), b)
		tr.Set(fdb.Key(indexKey), []byte{})
		return nil, nil
	})
	if err != nil {
		return fmt.Errorf("automation rule set: %w", err)
	}
	return nil
}

func (s *AutomationStore) Get(_ context.Context, orgID, ruleID uuid.UUID) (*node.AutomationRule, error) {
	key := automationRuleKey(orgID, ruleID)
	val, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(fdb.Key(key)).Get()
	})
	if err != nil {
		return nil, fmt.Errorf("automation rule get: %w", err)
	}
	b, ok := val.([]byte)
	if !ok || len(b) == 0 {
		return nil, nil
	}
	var rule node.AutomationRule
	if err := json.Unmarshal(b, &rule); err != nil {
		return nil, fmt.Errorf("unmarshal automation rule: %w", err)
	}
	return &rule, nil
}

func (s *AutomationStore) ListByTrigger(_ context.Context, orgID, workspaceID uuid.UUID, nodeType string, trigger node.AutomationTrigger) ([]*node.AutomationRule, error) {
	prefix := tuple.Tuple{keyAutomationRuleByTrigger, orgID.String(), workspaceID.String(), nodeType, string(trigger)}.Pack()
	pr, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, err
	}
	idxVals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("automation list by trigger index: %w", err)
	}
	kvs := idxVals.([]fdb.KeyValue)
	if len(kvs) == 0 {
		return nil, nil
	}

	// Collect ruleIDs then batch-fetch primary values.
	ruleIDs := extractLastUUIDs(kvs, 5)
	primaries, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		futures := make([]fdb.FutureByteSlice, len(ruleIDs))
		for i, id := range ruleIDs {
			futures[i] = tr.Get(fdb.Key(automationRuleKey(orgID, id)))
		}
		results := make([][]byte, len(futures))
		for i, f := range futures {
			b, err := f.Get()
			if err != nil {
				return nil, err
			}
			results[i] = b
		}
		return results, nil
	})
	if err != nil {
		return nil, fmt.Errorf("automation list hydrate: %w", err)
	}

	byteSlices := primaries.([][]byte)
	rules := make([]*node.AutomationRule, 0, len(byteSlices))
	for _, b := range byteSlices {
		if len(b) == 0 {
			continue
		}
		var rule node.AutomationRule
		if err := json.Unmarshal(b, &rule); err != nil {
			continue
		}
		if !rule.Enabled {
			continue
		}
		rules = append(rules, &rule)
	}
	return rules, nil
}

func (s *AutomationStore) Delete(_ context.Context, orgID, ruleID uuid.UUID) error {
	primaryKey := fdb.Key(automationRuleKey(orgID, ruleID))
	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		// Read inside the transaction to get the trigger index key for cleanup.
		b := tr.Get(primaryKey).MustGet()
		if len(b) > 0 {
			var rule node.AutomationRule
			if json.Unmarshal(b, &rule) == nil {
				tr.Clear(fdb.Key(automationTriggerIndexKey(orgID, rule.WorkspaceID, rule.NodeType, rule.Trigger, ruleID)))
			}
		}
		tr.Clear(primaryKey)
		return nil, nil
	})
	if err != nil {
		return fmt.Errorf("automation rule delete: %w", err)
	}
	return nil
}

func automationRuleKey(orgID, ruleID uuid.UUID) []byte {
	return tuple.Tuple{keyAutomationRule, orgID.String(), ruleID.String()}.Pack()
}

func automationTriggerIndexKey(orgID, workspaceID uuid.UUID, nodeType string, trigger node.AutomationTrigger, ruleID uuid.UUID) []byte {
	return tuple.Tuple{keyAutomationRuleByTrigger, orgID.String(), workspaceID.String(), nodeType, string(trigger), ruleID.String()}.Pack()
}
