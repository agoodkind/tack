package main

import (
	"net/http"
)

func registerConnectHandlers(mux *http.ServeMux, authMiddleware func(http.Handler) http.Handler) {
}
