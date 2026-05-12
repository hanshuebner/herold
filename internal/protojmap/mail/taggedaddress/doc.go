// Package taggedaddress implements the JMAP TaggedAddressFilter datatype
// handlers per docs/design/server/requirements/24-tagged-addresses.md
// (REQ-TAG-70..72, REQ-TAG-86..91):
//
//   - TaggedAddressFilter/get   — read filters for the calling principal.
//   - TaggedAddressFilter/set   — create / update / destroy filters.
//   - TaggedAddressFilter/changes — JMAP changes endpoint.
//
// The capability URI is "https://netzhansa.com/jmap/tagged-addresses"
// and is registered by internal/admin/server.go when
// [server.tagged_addresses].enabled is true (the gate already lives
// there from task #23/#28).
//
// State token: the state advances when any filter row is mutated. The
// store does not carry its own tagged-address state counter; per
// REQ-TAG-72 the SPA invalidates its tagged-address cache through the
// Identity/changes push. We therefore use the Identity JMAP state
// counter — bumping it on every TaggedAddressFilter/set mutation gives
// the JMAP /get and /changes endpoints a monotonic state token and at
// the same time arms the SPA's Identity-state-change consumers.
//
// Dismissals (REQ-TAG-60..62) do NOT live on the JMAP wire (REQ-TAG-71);
// they have a REST shape implemented elsewhere.
package taggedaddress
