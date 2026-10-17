package foundationdb

import (
	"context"
	"encoding/json"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// Property warning keys use separate families. A verified warning key uses
// (family, orgID, nodeID, property name). Its revision key uses (family,
// orgID, nodeID). A staged warning key places the work class before orgID.
const (
	keySearchWarning              = "search_warning"
	keySearchWarningRevision      = "search_warning_revision"
	keySearchWarningStaged        = "search_warning_staged"
	keySearchWarningStagedVersion = "search_warning_staged_version"
)

// stagedWarningVersion identifies the revision, generation, and projection
// of one staged warning set.
type stagedWarningVersion struct {
	Revision   int64  `json:"revision"`
	Generation int64  `json:"generation"`
	Projection string `json:"projection"`
}

func searchWarningPrefix(orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchWarning, orgID.String(), nodeID.String()}.Pack())
}

func searchWarningKey(orgID, nodeID uuid.UUID, propertyName string) []byte {
	return withPrefix(tuple.Tuple{keySearchWarning, orgID.String(), nodeID.String(), propertyName}.Pack())
}

// searchWarningFamilyPrefix returns the prefix for verified warning keys
// across all organizations.
func searchWarningFamilyPrefix() []byte {
	return withPrefix(tuple.Tuple{keySearchWarning}.Pack())
}

func searchWarningRevisionKey(orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchWarningRevision, orgID.String(), nodeID.String()}.Pack())
}

func stagedWarningPrefix(class string, orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchWarningStaged, class, orgID.String(), nodeID.String()}.Pack())
}

func stagedWarningKey(class string, orgID, nodeID uuid.UUID, propertyName string) []byte {
	return withPrefix(tuple.Tuple{keySearchWarningStaged, class, orgID.String(), nodeID.String(), propertyName}.Pack())
}

func stagedWarningVersionKey(class string, orgID, nodeID uuid.UUID) []byte {
	return withPrefix(tuple.Tuple{keySearchWarningStagedVersion, class, orgID.String(), nodeID.String()}.Pack())
}

// stagePropertyWarnings replaces the staged warnings for the claimed work
// class with the diagnostics from page ordinal 0. It ignores later pages,
// which repeat the revision's diagnostics.
func stagePropertyWarnings(ctx context.Context, tr fdb.Transaction, work searchdomain.Work, page node.ContentPage, revision int64) error {
	if page.Ordinal != 0 {
		return nil
	}
	class := string(work.Class)
	if err := clearPrefixRange(ctx, tr, stagedWarningPrefix(class, work.OrgID, work.NodeID)); err != nil {
		return err
	}
	for _, diagnostic := range page.Diagnostics {
		if err := writeWarningValue(ctx, tr, stagedWarningKey(class, work.OrgID, work.NodeID, diagnostic.PropertyName), diagnostic); err != nil {
			return err
		}
	}
	return writeWarningValue(ctx, tr, stagedWarningVersionKey(class, work.OrgID, work.NodeID), stagedWarningVersion{
		Revision: revision, Generation: work.Generation, Projection: page.ProjectionVersion,
	})
}

// writeWarningValue stores value as JSON at key.
func writeWarningValue[V node.PropertyDiagnostic | stagedWarningVersion](ctx context.Context, tr fdb.Transaction, key []byte, value V) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return searchReadFailure(ctx, "encode search warning", err)
	}
	tr.Set(fdb.Key(key), encoded)
	return nil
}

// promotePropertyWarnings replaces the node's verified warnings with the
// staged warnings for the claimed work class after indexing completes. It
// preserves the verified set if the staged revision or projection differs,
// or if the verified revision is newer. It then clears the staged set.
func promotePropertyWarnings(ctx context.Context, tr fdb.Transaction, work searchdomain.Work, revision int64, projection string) error {
	class := string(work.Class)
	encoded, err := tr.Get(fdb.Key(stagedWarningVersionKey(class, work.OrgID, work.NodeID))).Get()
	if err != nil {
		return searchReadFailure(ctx, "read staged warning version", err)
	}
	if len(encoded) == 0 {
		return nil
	}
	var staged stagedWarningVersion
	if err := json.Unmarshal(encoded, &staged); err != nil {
		return searchReadFailure(ctx, "decode staged warning version of node "+work.NodeID.String(), err)
	}
	if staged.Revision != revision || staged.Projection != projection {
		return nil
	}
	verified, err := readSearchCounter(ctx, tr, searchWarningRevisionKey(work.OrgID, work.NodeID))
	if err != nil {
		return err
	}
	if verified <= revision {
		if err := replaceVerifiedWarnings(ctx, tr, work, revision); err != nil {
			return err
		}
	}
	tr.Clear(fdb.Key(stagedWarningVersionKey(class, work.OrgID, work.NodeID)))
	return clearPrefixRange(ctx, tr, stagedWarningPrefix(class, work.OrgID, work.NodeID))
}

// replaceVerifiedWarnings copies the staged warnings for the claimed work
// class into the verified set and records revision as the verified revision.
func replaceVerifiedWarnings(ctx context.Context, tr fdb.Transaction, work searchdomain.Work, revision int64) error {
	stagedRange, err := fdb.PrefixRange(stagedWarningPrefix(string(work.Class), work.OrgID, work.NodeID))
	if err != nil {
		return searchReadFailure(ctx, "create staged warning range", err)
	}
	items, err := tr.GetRange(stagedRange, fdb.RangeOptions{}).GetSliceWithError()
	if err != nil {
		return searchReadFailure(ctx, "read staged warnings", err)
	}
	if err := clearPrefixRange(ctx, tr, searchWarningPrefix(work.OrgID, work.NodeID)); err != nil {
		return err
	}
	for _, item := range items {
		var diagnostic node.PropertyDiagnostic
		if err := json.Unmarshal(item.Value, &diagnostic); err != nil {
			return searchReadFailure(ctx, "decode staged warning of node "+work.NodeID.String(), err)
		}
		tr.Set(fdb.Key(searchWarningKey(work.OrgID, work.NodeID, diagnostic.PropertyName)), item.Value)
	}
	writeSearchCounter(tr, searchWarningRevisionKey(work.OrgID, work.NodeID), revision)
	return nil
}

// clearPropertyWarnings removes all verified and staged warning keys for a
// deleted node.
func clearPropertyWarnings(ctx context.Context, tr fdb.Transaction, orgID, nodeID uuid.UUID) error {
	if err := clearPrefixRange(ctx, tr, searchWarningPrefix(orgID, nodeID)); err != nil {
		return err
	}
	tr.Clear(fdb.Key(searchWarningRevisionKey(orgID, nodeID)))
	for _, class := range searchNodeClasses {
		tr.Clear(fdb.Key(stagedWarningVersionKey(string(class), orgID, nodeID)))
		if err := clearPrefixRange(ctx, tr, stagedWarningPrefix(string(class), orgID, nodeID)); err != nil {
			return err
		}
	}
	return nil
}

func clearPrefixRange(ctx context.Context, tr fdb.Transaction, prefix []byte) error {
	keyRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		return searchReadFailure(ctx, "create warning range", err)
	}
	tr.ClearRange(keyRange)
	return nil
}
