package protoadmin

import "net/http"

// WrapRecoverForTest exposes the internal panic-recover middleware to
// the _test package so behaviour can be asserted without constructing
// a network round-trip.
func WrapRecoverForTest(s *Server, next http.Handler) http.Handler {
	return s.withPanicRecover(next)
}

// Options returns the Options snapshot the Server was constructed with.
// Exposed for tests that need to inspect injected dependencies (e.g.
// the DKIMKeyManager stub in dkim_test.go).
func (s *Server) Options() Options {
	return s.opts
}
