package protoadmin

import "net/http"

// WrapRecoverForTest exposes the internal panic-recover middleware to
// the _test package so behaviour can be asserted without constructing
// a network round-trip.
func WrapRecoverForTest(s *Server, next http.Handler) http.Handler {
	return s.withPanicRecover(next)
}

// GenerateAPIKey exposes the internal generateAPIKey function to the
// _test package so tests can create pre-hashed API keys without
// duplicating the hashing logic.
func GenerateAPIKey() (plaintext, hash string, err error) {
	return generateAPIKey()
}

// Options returns the Options snapshot the Server was constructed with.
// Exposed for tests that need to inspect injected dependencies (e.g.
// the DKIMKeyManager stub in dkim_test.go).
func (s *Server) Options() Options {
	return s.opts
}

// IsBugReportPartName exposes the internal isBugReportPartName function
// to the _test package so a test can assert the server's bug-report
// part allow-list against the names the Android client's bundle builder
// can actually emit (issue #420).
func IsBugReportPartName(name string) bool {
	return isBugReportPartName(name)
}

// ProviderNameByTokenURL exposes the internal providerNameByTokenURL method
// to the _test package for unit testing the priority ordering and edge cases
// (re #131).
func (s *Server) ProviderNameByTokenURL(tokenURL string) string {
	return s.providerNameByTokenURL(tokenURL)
}
