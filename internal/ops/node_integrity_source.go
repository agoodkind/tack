package ops

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
)

// repairParentProperty identifies the property for the canonical parent ID.
const repairParentProperty = "parent_id"

// RepairEvidenceReader reads a repair's source state within a single transaction.
type RepairEvidenceReader interface {
	RepairEvidence(ctx context.Context, request fdbadapter.RepairEvidenceRequest) (fdbadapter.RepairEvidence, error)
}

// primaryDigest returns the SHA-256 digest of primary after JSON compaction.
// Compact records retain their exact byte digest. If compaction fails, it
// hashes the original bytes.
func primaryDigest(primary []byte) string {
	var compact bytes.Buffer
	if err := json.Compact(&compact, primary); err != nil {
		compact.Reset()
		compact.Write(primary)
	}
	sum := sha256.Sum256(compact.Bytes())
	return hex.EncodeToString(sum[:])
}

// readRepairEvidence reads entry's node, edges, resulting parent chain,
// and added edge targets.
func readRepairEvidence(ctx context.Context, reader RepairEvidenceReader, entry NodeIntegrityEntry) (fdbadapter.RepairEvidence, error) {
	request := fdbadapter.RepairEvidenceRequest{NodeID: entry.NodeID, ParentID: resultParent(entry.Result), Related: nil}
	for _, edge := range entry.Result.AddEdges {
		request.Related = append(request.Related, edge.TargetID)
	}
	evidence, err := reader.RepairEvidence(ctx, request)
	if err != nil {
		wrapped := fmt.Errorf("read evidence of repair %s for node %s: %w", entry.RepairID, entry.NodeID, err)
		slog.ErrorContext(ctx, "repair.evidence.read_failed", slog.String("err", wrapped.Error()), slog.String("repair_id", entry.RepairID))
		return evidence, wrapped
	}
	return evidence, nil
}

// resultParent parses parent_id from result.Props and returns [uuid.Nil]
// when the property does not contain a valid UUID string.
func resultParent(result RepairResult) uuid.UUID {
	var parentText string
	if json.Unmarshal(result.Props[repairParentProperty], &parentText) != nil {
		return uuid.Nil
	}
	parentID, err := uuid.Parse(parentText)
	if err != nil {
		return uuid.Nil
	}
	return parentID
}

// sourceFromEvidence converts current evidence into an entry's frozen source.
func sourceFromEvidence(ctx context.Context, evidence fdbadapter.RepairEvidence) (RepairSource, error) {
	source := RepairSource{
		Primary: json.RawMessage(evidence.Node.Primary), PrimaryDigest: primaryDigest(evidence.Node.Primary),
		Edges: storedEdges(evidence), Related: make([]RepairRelated, 0, len(evidence.Related)),
		Chain: evidence.Chain, GraphDigest: "",
		MetadataEpoch: evidence.Node.OrgID.String() + "=" + strconv.FormatInt(evidence.MetadataEpoch, 10),
	}
	if source.Chain == nil {
		source.Chain = []uuid.UUID{}
	}
	for _, record := range evidence.Related {
		related := RepairRelated{NodeID: record.NodeID, PrimaryDigest: ""}
		if record.Found && record.OrgID == evidence.Node.OrgID {
			related.PrimaryDigest = primaryDigest(record.Primary)
		}
		source.Related = append(source.Related, related)
	}
	digest, err := graphDigest(ctx, source, evidence.Cycle)
	if err != nil {
		return source, err
	}
	source.GraphDigest = digest
	return source, nil
}

func storedEdges(evidence fdbadapter.RepairEvidence) []RepairStoredEdge {
	edges := make([]RepairStoredEdge, 0, len(evidence.Outgoing)+len(evidence.Incoming))
	for _, records := range [][]fdbadapter.RepairEdgeRecord{evidence.Outgoing, evidence.Incoming} {
		for _, record := range records {
			edges = append(edges, RepairStoredEdge{SourceID: record.SourceID, RelationType: record.RelationType, TargetID: record.TargetID})
		}
	}
	slices.SortFunc(edges, func(left, right RepairStoredEdge) int {
		return strings.Compare(left.SourceID.String()+left.RelationType+left.TargetID.String(),
			right.SourceID.String()+right.RelationType+right.TargetID.String())
	})
	return edges
}

// repairGraph contains the fields used to calculate the graph digest.
type repairGraph struct {
	Edges   []RepairStoredEdge `json:"edges"`
	Related []RepairRelated    `json:"related"`
	Chain   []uuid.UUID        `json:"chain"`
	Cycle   bool               `json:"cycle"`
}

func graphDigest(ctx context.Context, source RepairSource, cycle bool) (string, error) {
	encoded, err := json.Marshal(repairGraph{Edges: source.Edges, Related: source.Related, Chain: source.Chain, Cycle: cycle})
	if err != nil {
		wrapped := fmt.Errorf("encode repair graph: %w", err)
		slog.ErrorContext(ctx, "repair.graph.encode_failed", slog.String("err", wrapped.Error()))
		return "", wrapped
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
