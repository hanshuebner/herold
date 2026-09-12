// Command heroldfakefcm is a standalone deterministic Firebase Cloud
// Messaging HTTP v1 messages:send endpoint for dev instances. It implements
// the same fake FCM server as internal/testfakes/fakefcm but as an
// independent process that scripts/dev-instance.sh can build and manage
// (re #334, re #200).
//
// On startup it binds to a kernel-picked port on 127.0.0.1, writes a
// key=value report file (--report-file), and blocks until SIGTERM or
// SIGINT. The report file contains the endpoint URL needed by
// dev-instance.sh to configure the herold instance's
// [server.push] fcm_base_url.
//
// Usage:
//
//	heroldfakefcm [--project-id ID] --report-file PATH
package main
