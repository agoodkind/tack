package integration_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const (
	partRestoreLargeToolBytes = 70000
	partRestoreSizeLimit      = 65536
	repeatedKeyFragment       = "repeats a JSON object key"
	commitVersionFragment     = "commit_version that is not a string"
	partRestoreOtherDigest    = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
)

func headerAndPartsSet(name, fragment, path, value string) partRestoreMutation {
	return partRestoreMutation{name: name, fragment: fragment, edit: func(t *testing.T, e *changeEncoding, other string) {
		t.Helper()
		raw := json.RawMessage(strings.ReplaceAll(value, otherToken, other))
		e.editHeader(t, path, raw)
		e.editEveryPart(t, path, raw)
	}}
}

func partSet(name, fragment string, index int, path, value string) partRestoreMutation {
	return partRestoreMutation{name: name, fragment: fragment, edit: func(t *testing.T, e *changeEncoding, other string) {
		t.Helper()
		e.editPart(t, index, path, json.RawMessage(strings.ReplaceAll(value, otherToken, other)))
	}}
}

func partDrop(name, fragment string, index int, path string) partRestoreMutation {
	return partRestoreMutation{name: name, fragment: fragment, edit: func(t *testing.T, e *changeEncoding, _ string) {
		t.Helper()
		e.editPart(t, index, path, nil)
	}}
}

func repackagedEnvelope(name, fragment string, chunk int, rewrite func(envelope []byte) []byte) partRestoreMutation {
	return partRestoreMutation{name: name, fragment: fragment, edit: func(t *testing.T, e *changeEncoding, _ string) {
		t.Helper()
		e.repackage(t, rewrite(e.envelope(t)), chunk)
	}}
}

func replaceFirst(from, to string) func(envelope []byte) []byte {
	return func(envelope []byte) []byte {
		return bytes.Replace(envelope, []byte(from), []byte(to), 1)
	}
}

func rawPartReplace(name, fragment, from, to string) partRestoreMutation {
	return partRestoreMutation{name: name, fragment: fragment, edit: func(t *testing.T, e *changeEncoding, _ string) {
		t.Helper()
		if !bytes.Contains(e.parts[0], []byte(from)) {
			t.Fatalf("part 0 does not contain %s", from)
		}
		e.parts[0] = bytes.Replace(e.parts[0], []byte(from), []byte(to), 1)
	}}
}

func decimalShard() partRestoreMutation {
	return partRestoreMutation{name: "decimal shard", fragment: integerFragment, edit: func(t *testing.T, e *changeEncoding, _ string) {
		t.Helper()
		var members map[string]json.RawMessage
		if err := json.Unmarshal(e.header, &members); err != nil {
			t.Fatalf("decode the manifest: %v", err)
		}
		shard := json.RawMessage(string(members["shard"]) + ".0")
		e.editHeader(t, "shard", shard)
		e.editEveryPart(t, "shard", shard)
	}}
}

func transportMutations() []partRestoreMutation {
	const (
		manifestFragment = "not a version 1 event manifest"
		partKindFragment = "is not a version 1 event part"
		matchFragment    = "does not match the manifest event, organization, shard, part count, or transport digest"
		envelopeFragment = "not an event followed by an empty read target list"
		otherUUID        = `"{other}"`
	)
	appendTarget := func(envelope []byte) []byte {
		return bytes.Replace(envelope, []byte(`"read_targets":[]`), []byte(`"read_targets":[{"kind":"node"}]`), 1)
	}
	reorder := func(envelope []byte) []byte {
		members := bytes.TrimPrefix(bytes.TrimSuffix(envelope, []byte(`,"read_targets":[]}`)), []byte("{"))
		return append(append([]byte(`{"read_targets":[],`), members...), '}')
	}
	return []partRestoreMutation{
		headerSet("manifest format version", manifestFragment, "format_version", "2"),
		headerSet("manifest message kind", manifestFragment, "audit_message", `"event_part"`),
		headerSet("manifest unknown field", "change manifest has a field", "surprise", "true"),
		headerSet("manifest event ID", "names another event ID", "event_id", otherUUID),
		headerSet("manifest organization", "names another organization", "org_id", otherUUID),
		headerSet("manifest part count", "part count differs from the supplied parts", "part_count", "5"),
		headerSet("manifest digest not hexadecimal", "not a SHA-256 hex digest", "transport_digest", `"zz"`),
		headerSet("manifest transport digest only", matchFragment, "transport_digest", `"`+partRestoreOtherDigest+`"`),
		headerAndPartsSet("transport digest", "do not match the manifest transport digest", "transport_digest", `"`+partRestoreOtherDigest+`"`),
		headerSet("manifest shard only", matchFragment, "shard", "300"),
		headerAndPartsSet("shard", "shard is not the shard of the actor and event", "shard", "300"),
		partSet("part format version", partKindFragment, 0, "format_version", "2"),
		partSet("part message kind", partKindFragment, 0, "audit_message", `"event_manifest"`),
		partSet("part unknown field", "change part 0 has a field", 0, "surprise", "true"),
		partSet("part event ID", matchFragment, 0, "event_id", otherUUID),
		partSet("part organization", matchFragment, 0, "org_id", otherUUID),
		partSet("part shard", matchFragment, 0, "shard", "300"),
		partSet("part count", matchFragment, 0, "part_count", "5"),
		partSet("part transport digest", matchFragment, 0, "transport_digest", `"`+partRestoreOtherDigest+`"`),
		partSet("second part index repeated", "change part 1 is not the part at that index", 1, "part_index", "0"),
		partSet("second part index skipped", "change part 1 is not the part at that index", 1, "part_index", "5"),
		partDrop("part payload omitted", "does not have a payload", 0, "payload"),
		partSet("part payload not base64", "does not decode", 0, "payload", `"not base64!"`),
		headerSet("manifest decimal format version", integerFragment, "format_version", "1.0"),
		headerAndPartsSet("decimal part count", integerFragment, "part_count", "2.0"),
		partSet("part decimal format version", integerFragment, 0, "format_version", "1.0"),
		partSet("second part decimal index", integerFragment, 1, "part_index", "1.0"),
		decimalShard(),
		rawPartReplace("part repeats format version, decimal last", repeatedKeyFragment, `"format_version":1,`, `"format_version":1,"format_version":1.0,`),
		rawPartReplace("part repeats format version, decimal first", repeatedKeyFragment, `"format_version":1,`, `"format_version":1.0,"format_version":1,`),
		repackagedEnvelope("envelope repeats audit format version", repeatedKeyFragment, partRestoreChunkBytes, replaceFirst(`"audit_format_version":2`, `"audit_format_version":2.0,"audit_format_version":2`)),
		repackagedEnvelope("envelope repeats actor email", repeatedKeyFragment, partRestoreChunkBytes, replaceFirst(`"actor":{`, `"actor":{"email":7,"email":"a@b.test",`)),
		headerSet("manifest commit version type", commitVersionFragment, "commit_version", "7"),
		partSet("part commit version type", commitVersionFragment, 0, "commit_version", "7"),
		partSet("part digest", "does not match its digest", 0, "part_digest", `"`+partRestoreOtherDigest+`"`),
		repackagedEnvelope("part larger than the size limit", "larger than the part size limit", 2*partRestoreSizeLimit, func(envelope []byte) []byte { return envelope }),
		repackagedEnvelope("read target listed", envelopeFragment, partRestoreChunkBytes, appendTarget),
		repackagedEnvelope("envelope members reordered", envelopeFragment, partRestoreChunkBytes, reorder),
	}
}

func TestAuditPartRestoreRefusesEveryTransportField(t *testing.T) {
	pipeline := startPartRestorePipeline(t)
	tool := strings.Repeat("t", partRestoreLargeToolBytes)
	_, built := pipeline.builtChange(t, tool)
	if len(built.parts) < 2 {
		t.Fatalf("multipart change event has %d parts, want at least 2", len(built.parts))
	}
	for _, current := range transportMutations() {
		t.Run(current.name, func(t *testing.T) {
			pipeline.requireRefusedChange(t, tool, current)
		})
	}
	t.Run("valid multipart change event is projected into the ledger", func(t *testing.T) {
		pipeline.requireAcceptedChange(t, tool)
	})
}
