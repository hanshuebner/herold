// Package protojmap implements JMAP Core (RFC 8620) and the dispatch
// surface that JMAP datatypes (Mail, Submission, Identity, ...) plug
// into.
//
// The package owns:
//
//   - The session endpoint (GET /.well-known/jmap), the request /
//     response envelope (POST /jmap), the upload / download surface
//     (POST /jmap/upload, GET /jmap/download), and the EventSource
//     push channel (GET /jmap/eventsource).
//   - A CapabilityRegistry that per-datatype packages register
//     MethodHandler implementations against. The dispatch core does
//     not enumerate concrete types — datatype handlers register at
//     server construction time, the registry is the seam.
//
// Datatype implementations (Mailbox, Email, EmailSubmission, ...)
// live in their own packages. Each registers (capability, handlers)
// with the Server's registry before Serve is called.
//
// Ownership: jmap-implementor.
package protojmap
