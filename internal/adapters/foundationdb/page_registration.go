package foundationdb

import (
	"context"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

func (s *SearchWorkStore) verifyRegistration(
	ctx context.Context,
	transaction fdb.Transaction,
	work searchdomain.Work,
	page node.ContentPage,
	revision int64,
) error {
	access, err := s.verifyPage(ctx, transaction, work, page, revision)
	if err != nil {
		return err
	}

	tracked := trackRegisteredAccess(access, page.Access)
	if !samePageTracking(access, tracked) {
		if err := writeSearchRecord(
			ctx,
			transaction,
			searchAccessKey(work.OrgID, work.NodeID),
			tracked,
		); err != nil {
			return err
		}
	}

	if err := requireOneProjection(ctx, transaction, work, page, revision); err != nil {
		return err
	}

	if err := requireActiveProjection(
		ctx,
		transaction,
		work.OrgID,
		page.ProjectionVersion,
	); err != nil {
		return err
	}

	if err := stagePropertyWarnings(ctx, transaction, work, page, revision); err != nil {
		return err
	}

	return nil
}
