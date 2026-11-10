package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// errManifestPartStore marks a database error while writing a permanent read
// manifest part. streamAssembly returns it without rejecting the transport.
var errManifestPartStore = errors.New("store read manifest part")

// assemblyStream decodes one event envelope from the stored parts of an
// assembly. It writes each permanent read manifest part as soon as the part
// has its targets and keeps at most one stored part and one manifest part in
// memory.
type assemblyStream struct {
	reader  *assemblyPartReader
	decoder *json.Decoder
	// write stores one permanent read manifest part of event.
	write    func(event Event, partIndex int64, payload []byte) error
	event    Event `exhaustruct:"optional"`
	targets  int
	parts    uint32
	manifest hash.Hash
}

// streamAssembly decodes the event in the stored parts of eventID and checks
// the parts, transport digest, envelope identity, and read evidence. It writes
// the read manifest parts in tx and returns the event and the transport
// digest. A malformed assembly returns a transport rejection.
func streamAssembly(ctx context.Context, tx pgx.Tx, eventID uuid.UUID, header []byte) (Event, string, error) {
	manifest, err := decodeManifestHeader(header)
	if err != nil {
		return Event{}, "", rejectTransport(ctx, eventID, err.Error())
	}
	reader := newAssemblyPartReader(ctx, tx, manifest)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	stream := &assemblyStream{
		reader: reader, decoder: decoder, write: manifestPartWriter(ctx, tx), targets: 0, parts: 0, manifest: sha256.New(),
	}
	if err := stream.decode(); err != nil {
		if errors.Is(err, errManifestPartStore) {
			return Event{}, "", err
		}
		if failure := reader.databaseFailure(); failure != nil {
			return Event{}, "", failure
		}
		return Event{}, "", rejectTransport(ctx, eventID, err.Error())
	}
	if !reader.complete() {
		return Event{}, "", rejectTransport(ctx, eventID, "transport digest does not match the parts")
	}
	if err := stream.checkReadEvidence(); err != nil {
		return Event{}, "", rejectTransport(ctx, eventID, err.Error())
	}
	return stream.event, manifest.TransportDigest, nil
}

// decode reads the envelope object with the event member first and the
// read_targets member second, then requires the end of the stream.
func (s *assemblyStream) decode() error {
	if err := s.expectDelimiter('{'); err != nil {
		return err
	}
	if err := s.expectKey("event"); err != nil {
		return err
	}
	if err := s.decoder.Decode(&s.event); err != nil {
		return fmt.Errorf("decode envelope event: %s", err.Error())
	}
	header := s.reader.manifest
	if s.event.EventID != header.EventID || s.event.Context.OrgID != header.OrgID || shardOf(s.event.Actor.ID, s.event.EventID) != header.Shard {
		return errors.New("envelope identity does not match the header")
	}
	if err := s.expectKey("read_targets"); err != nil {
		return err
	}
	if err := s.decodeTargets(); err != nil {
		return err
	}
	if err := s.expectDelimiter('}'); err != nil {
		return err
	}
	if !decoderAtEnd(s.decoder) {
		return errors.New("unexpected data after the event envelope")
	}
	return nil
}

// decodeTargets decodes the read target array one target at a time and groups
// the targets into manifest parts.
func (s *assemblyStream) decodeTargets() error {
	if err := s.expectDelimiter('['); err != nil {
		return err
	}
	builder := newManifestBuilder(s.storeManifestPart)
	for s.decoder.More() {
		if s.event.Context.Read == nil {
			return errors.New("has read targets without read evidence")
		}
		var target Target
		if err := s.decoder.Decode(&target); err != nil {
			return fmt.Errorf("decode read target: %s", err.Error())
		}
		if err := builder.add(target); err != nil {
			return err
		}
		s.targets++
	}
	if err := builder.finish(); err != nil {
		return err
	}
	return s.expectDelimiter(']')
}

// storeManifestPart adds payload to the manifest digest and writes it as the
// next permanent read manifest part when the event declares manifest parts.
func (s *assemblyStream) storeManifestPart(payload []byte) error {
	_, _ = s.manifest.Write(payload)
	partIndex := int64(s.parts)
	s.parts++
	read := s.event.Context.Read
	if read == nil || read.ManifestParts == 0 {
		return nil
	}
	return s.write(s.event, partIndex, payload)
}

// manifestPartWriter returns a function that inserts one permanent read
// manifest part in tx and keeps an existing part with the same index. A
// database error wraps errManifestPartStore.
func manifestPartWriter(ctx context.Context, tx pgx.Tx) func(event Event, partIndex int64, payload []byte) error {
	return func(event Event, partIndex int64, payload []byte) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit.event_parts (org_id, event_id, part_index, digest, payload)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (org_id, event_id, part_index) DO NOTHING
		`, event.Context.OrgID, event.EventID, partIndex, sha256Hex(payload), payload); err != nil {
			slog.ErrorContext(ctx, "audit.consumer.manifest_store_failed", slog.String("event_id", event.EventID.String()), slog.String("err", err.Error()))
			return fmt.Errorf("%w: audit event %s read manifest part %d: %w", errManifestPartStore, event.EventID, partIndex, err)
		}
		return nil
	}
}

// checkReadEvidence compares the manifest digest and part count with the read
// evidence of the event, as checkReadManifest does.
func (s *assemblyStream) checkReadEvidence() error {
	read := s.event.Context.Read
	if read == nil || (s.targets == 0 && read.ManifestSHA256 == "" && read.ManifestParts == 0) {
		return nil
	}
	if hex.EncodeToString(s.manifest.Sum(nil)) != read.ManifestSHA256 || s.parts != read.ManifestParts {
		return errors.New("has read evidence that does not match its read targets")
	}
	return nil
}

// expectDelimiter reads the next token and requires the JSON delimiter want.
func (s *assemblyStream) expectDelimiter(want json.Delim) error {
	token, err := s.decoder.Token()
	if err != nil {
		return fmt.Errorf("decode envelope: %s", err.Error())
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != want {
		return fmt.Errorf("decode envelope: found %q instead of %q", fmt.Sprint(token), want.String())
	}
	return nil
}

// expectKey reads the next token and requires the object member name want.
func (s *assemblyStream) expectKey(want string) error {
	token, err := s.decoder.Token()
	if err != nil {
		return fmt.Errorf("decode envelope: %s", err.Error())
	}
	if key, ok := token.(string); !ok || key != want {
		return fmt.Errorf("decode envelope: found member %q instead of %q", fmt.Sprint(token), want)
	}
	return nil
}
