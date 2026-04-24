// Package storetest is the shared integration-test harness for store
// backends. Every backend under internal/store{sqlite,pg} invokes Run to
// exercise the same compliance matrix: principals, mailboxes, messages,
// CONDSTORE, the change-feed, blob IO, quotas, migrations replay, and
// sentinel-error surfaces. This is the "single test matrix, both
// backends" pattern that STANDARDS.md §1.8 mandates.
//
// Ownership: storage-implementor. Imported only from *_test.go.
package storetest
