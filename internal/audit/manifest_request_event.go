package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ReadEventByRequest finds one organization's read event for a request.
func (r *Reader) ReadEventByRequest(
	ctx context.Context,
	orgID uuid.UUID,
	requestID string,
	oldest time.Time,
	latest time.Time,
) (uuid.UUID, error) {
	if orgID == uuid.Nil || requestID == "" || oldest.IsZero() || latest.IsZero() {
		return uuid.Nil, fmt.Errorf(
			"read event lookup requires an organization, request ID, and time range",
		)
	}

	rows, err := r.pool.Rows(
		r.pool.unsanitized.Query(
			ctx,
			readStatement("manifest_request_event"),
			orgID,
			oldest,
			latest,
			requestID,
		),
	)
	if err != nil {
		return uuid.Nil, fmt.Errorf("cannot find the read event for the requested organization")
	}
	defer rows.Close()
	if !rows.Next() {
		if rows.Err() != nil {
			return uuid.Nil, fmt.Errorf("cannot find the read event for the requested organization")
		}

		return uuid.Nil, nil
	}

	var eventID uuid.UUID
	if err := rows.Scan(&eventID); err != nil {
		return uuid.Nil, fmt.Errorf("cannot decode the read event identity")
	}

	if rows.Next() {
		return uuid.Nil, fmt.Errorf(
			"multiple read events match the requested organization and request ID",
		)
	}

	if err := rows.Err(); err != nil {
		return uuid.Nil, fmt.Errorf("cannot find the read event for the requested organization")
	}

	return eventID, nil
}
