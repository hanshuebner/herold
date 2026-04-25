package protojmap

// HashAPIKeyForTest exposes the package-internal hash function so the
// test fixture can mint API keys without going through protoadmin's
// surface. Test-only by virtue of the _test.go filename.
func HashAPIKeyForTest(plaintext string) string { return hashAPIKey(plaintext) }
