// Package contacts wires the JMAP Contacts datatype handlers into the
// shared CapabilityRegistry owned by the protojmap Core dispatcher.
//
// REQ-PROTO-55 binds two specs together:
//
//   - RFC 9553 (JSContact) — the on-the-wire JSON object model for
//     contact data. Stable; the wire shape is the JSContact `Card`.
//   - draft-ietf-jmap-contacts — the JMAP binding that names the
//     AddressBook and Contact datatypes, the method families
//     (`AddressBook/get|changes|set|query|queryChanges`,
//     `Contact/get|changes|set|query|queryChanges`), and the
//     `urn:ietf:params:jmap:contacts` capability descriptor shape.
//
// Targeting draft-ietf-jmap-contacts-09 (2024-08). Server behaviour MAY
// drift if the draft revs; the doc.go pin lets reviewers spot drift at
// the import boundary. RFC 9553 is the JSContact ground truth and
// stable.
//
// Package layout. We use the simpler single-package layout (one Go
// package, multiple files) rather than the per-method-family
// sub-packages that internal/protojmap/mail uses. Reasons:
//
//  1. AddressBook and Contact share the same id-encoding helpers,
//     account-resolution helper, principal helper, and JSContact
//     serializer; one package keeps them visible without an exports
//     dance.
//  2. The handler set is small enough (10 methods) that a flat file
//     list reads more clearly than a deeply nested tree.
//  3. The JSContact serializer is the load-bearing piece; it lives at
//     the top of the package (jscontact.go) and the handlers reach
//     for it directly.
//
// File layout:
//
//   - doc.go              — this file: overview + binding-draft pin.
//   - jscontact.go        — Card struct + (de)serializer + helpers
//     (PrimaryEmail, DisplayName, ...).
//   - jscontact_test.go   — round-trip / validation tests.
//   - types.go            — wire-shape AddressBook / Contact structs,
//     id helpers, myRights envelope.
//   - state.go            — JMAP state-string encoding.
//   - helpers.go          — principal/account/serverFail helpers.
//   - addressbook.go      — AddressBook/get|changes|set|query|queryChanges
//     handlers.
//   - addressbook_test.go — AddressBook handler tests.
//   - contact.go          — Contact/get|changes|set|query|queryChanges
//     handlers.
//   - contact_test.go     — Contact handler tests.
//   - register.go         — Register installs every handler + the
//     capability descriptor.
//   - test_helpers.go     — fixture for the *_test.go files.
//
// Capability id is `urn:ietf:params:jmap:contacts`
// (protojmap.CapabilityJMAPContacts). Per-account capability descriptor
// advertises the binding-draft limits: maxAddressBooksPerAccount,
// maxContactsPerAddressBook, maxSizePerContactBlob.
package contacts
