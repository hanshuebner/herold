// Package protosmtp implements the SMTP inbound (relay + submission)
// state machine and the associated ESMTP extension surface listed in
// docs/design/server/requirements/01-protocols.md §SMTP.
//
// One Server covers all three listener shapes (relay-in on 25,
// submission STARTTLS on 587, submission implicit TLS on 465). The
// Server is constructed once at process start and fed accepted sockets
// via Serve. Each accepted connection runs in its own goroutine, gated
// by the Server's concurrency semaphore.
//
// Outbound SMTP (queue + DKIM signing + MTA-STS) is Phase 2 and is not
// implemented by this package.
//
// Ownership: smtp-implementor.
package protosmtp
