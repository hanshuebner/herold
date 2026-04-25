// Package vacation implements the JMAP VacationResponse datatype
// handlers per RFC 8621 §9. VacationResponse is a singleton: a single
// VacationResponse object exists per Account, with the well-known id
// "singleton".
//
// Persistence is via the per-principal Sieve script (REQ-PROTO-46):
// the JMAP VacationResponse object's fields map directly onto a
// `vacation` action at the top level of the script. Reading / writing
// the JMAP object means parsing / synthesising that action through the
// public sieve.Parse API.
//
// Edge case: a hand-written Sieve script that embeds vacation inside
// conditional branches cannot be safely round-tripped through this
// surface. /set returns a `forbidden` error in that case, advising the
// operator to edit the Sieve script directly via ManageSieve.
package vacation
