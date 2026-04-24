package protoadmin

import "net/http"

// WrapRecoverForTest exposes the internal panic-recover middleware to
// the _test package so behaviour can be asserted without constructing
// a network round-trip.
func WrapRecoverForTest(s *Server, next http.Handler) http.Handler {
	return s.withPanicRecover(next)
}
