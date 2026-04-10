package connectrpc

import (
	"connectrpc.com/connect"
	"goodkind.io/tack/internal/domain"
)

func domainErr(err error) error {
	switch err {
	case domain.ErrNotFound:
		return connect.NewError(connect.CodeNotFound, err)
	case domain.ErrAlreadyExists:
		return connect.NewError(connect.CodeAlreadyExists, err)
	case domain.ErrPermissionDenied:
		return connect.NewError(connect.CodePermissionDenied, err)
	case domain.ErrInvalidArgument:
		return connect.NewError(connect.CodeInvalidArgument, err)
	case domain.ErrFailedPrecondition:
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case domain.ErrUnauthenticated:
		return connect.NewError(connect.CodeUnauthenticated, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
