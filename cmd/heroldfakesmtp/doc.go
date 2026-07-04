// Command heroldfakesmtp is a standalone SMTP submission sink for dev
// instances. It implements the same fake SMTP server as
// internal/testfakes/fakesmtp but as an independent process that
// scripts/dev-instance.sh can build and manage.
//
// On startup it binds to kernel-picked ports on 127.0.0.1 (one SMTP, one
// HTTP status), writes a key=value report file (--report-file), and blocks
// until SIGTERM or SIGINT. Each accepted SMTP message is logged to stderr.
//
// The HTTP status server exposes:
//   - GET /messages — JSON array of accepted message envelopes.
//   - GET /count    — JSON {"count": N} of accepted messages.
//
// Usage:
//
//	heroldfakesmtp [--hostname NAME] --report-file PATH
package main
