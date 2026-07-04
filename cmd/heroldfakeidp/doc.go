// Command heroldfakeidp is a standalone deterministic OAuth 2.0 / OIDC
// authorization server for dev instances. It implements the same fake IdP as
// internal/testfakes/fakeidp but as an independent process that
// scripts/dev-instance.sh can build and manage.
//
// On startup it binds to a kernel-picked port on 127.0.0.1, writes a
// key=value report file (--report-file), and blocks until SIGTERM or SIGINT.
// The report file contains the URLs and credentials needed by dev-instance.sh
// to configure the herold instance's [server.oauth_providers] block.
//
// Usage:
//
//	heroldfakeidp [--client-id ID] [--client-secret SECRET] --report-file PATH
package main
