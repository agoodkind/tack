package audit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/google/uuid"
)

// manifestDeclaration contains bounded metadata from the selected event.
type manifestDeclaration struct {
	raw     []byte
	format  string
	class   string
	phase   string
	outcome string
}

// readEvidence validates the event declaration and rejects missing read
// evidence.
func (d manifestDeclaration) readEvidence(eventID uuid.UUID) (*ReadEvidence, error) {
	if len(d.raw) > EventPartMaxBytes {
		return nil, fmt.Errorf("event %s declares oversized read evidence", eventID)
	}
	format, err := strconv.ParseUint(d.format, 10, 16)
	if err != nil || (format != uint64(AuditFormatLegacy) && format != uint64(AuditFormatComplete)) {
		return nil, fmt.Errorf("event %s declares an invalid audit format", eventID)
	}
	var read *ReadEvidence
	if d.raw != nil {
		decoder := json.NewDecoder(bytes.NewReader(d.raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&read); err != nil || read == nil || !decoderAtEnd(decoder) {
			return nil, fmt.Errorf("event %s does not declare valid read evidence", eventID)
		}
	}
	if format == uint64(AuditFormatComplete) {
		var event Event
		event.Context.OperationClass = OperationClass(d.class)
		event.Context.Phase = Phase(d.phase)
		event.Context.Read = read
		event.Outcome = Outcome(d.outcome)
		if validatePhase(event.Context.OperationClass, event.Context.Phase, event.Outcome) != nil {
			return nil, fmt.Errorf("event %s declares invalid read evidence", eventID)
		}
		if (event.Context.OperationClass == OperationRead || read != nil) && validateClassEvidence(event) != nil {
			return nil, fmt.Errorf("event %s declares invalid read evidence", eventID)
		}
	} else if read != nil {
		return nil, fmt.Errorf("event %s declares invalid read evidence for its audit format", eventID)
	}
	if read == nil {
		return nil, fmt.Errorf("event %s did not record read evidence; manifest verification is unavailable", eventID)
	}
	if read.Count == 0 && read.ManifestParts == 0 && read.ManifestSHA256 == "" {
		return nil, fmt.Errorf("event %s did not record a manifest declaration; manifest verification is unavailable", eventID)
	}
	return read, nil
}
