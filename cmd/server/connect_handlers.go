package main

import (
	"net/http"

	connect "connectrpc.com/connect"
	"goodkind.io/tack/internal/telemetry"
)

type connectHandlerFactory func(...connect.HandlerOption) (string, http.Handler)

func registerConnectHandlers(mux *http.ServeMux, authMiddleware func(http.Handler) http.Handler) {
}

func mountConnectHandler(
	mux *http.ServeMux,
	authMiddleware func(http.Handler) http.Handler,
	factory connectHandlerFactory,
) {
	path, handler := factory(connect.WithInterceptors(telemetry.ConnectUnaryInterceptor()))
	mux.Handle(path, authMiddleware(handler))
}
