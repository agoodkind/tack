package integration_test

import (
	"net/http"
	"strings"
)

// armDelete starts keeping the first point in time delete request open.
func (proxy *predictHoldProxy) armDelete() {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	proxy.deleteArmed = true
}

// keepsOpen reports whether the proxy keeps request open until release.
func (proxy *predictHoldProxy) keepsOpen(request *http.Request) bool {
	if request.Method == http.MethodDelete && strings.HasSuffix(request.URL.Path, pitDeletePath) {
		return proxy.holdDelete()
	}

	return strings.HasPrefix(request.URL.Path, predictPathPrefix) &&
		proxy.hold(proxy.matches(request))
}

// holdDelete reports whether the proxy keeps a point in time delete request
// open. Only the first one after armDelete stays open.
func (proxy *predictHoldProxy) holdDelete() bool {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	if !proxy.deleteArmed || proxy.holding {
		return false
	}

	proxy.holding = true
	close(proxy.held)

	return true
}
