// Command heroldfakeunsubscribe runs a standalone fakeunsubscribe
// server for scripts/dev-instance.sh (issue #412). It writes a
// key=value report file (base_url, success_url, failure_url, port,
// cert_file) so the shell script can wire a seeded message's
// List-Unsubscribe header at it and make the herold server process
// trust its self-signed certificate.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/hanshuebner/herold/internal/testfakes/fakeunsubscribe"
)

func main() {
	var (
		failStatus = flag.Int("fail-status", 0, "HTTP status POST /fail returns (default 500)")
		reportFile = flag.String("report-file", "", "path to write key=value report (required)")
		certFile   = flag.String("cert-file", "", "path to write the server's self-signed certificate PEM (required)")
	)
	flag.Parse()

	if *reportFile == "" {
		fmt.Fprintln(os.Stderr, "heroldfakeunsubscribe: --report-file is required")
		os.Exit(1)
	}
	if *certFile == "" {
		fmt.Fprintln(os.Stderr, "heroldfakeunsubscribe: --cert-file is required")
		os.Exit(1)
	}

	srv, err := fakeunsubscribe.NewServer(fakeunsubscribe.Options{FailStatus: *failStatus})
	if err != nil {
		log.Fatalf("heroldfakeunsubscribe: start: %v", err)
	}
	defer srv.Close()

	if err := os.WriteFile(*certFile, srv.CertPEM(), 0o600); err != nil {
		log.Fatalf("heroldfakeunsubscribe: write cert file: %v", err)
	}

	// Write the report file atomically so dev-instance.sh can use
	// wait_for_file to detect readiness without a race.
	tmp := *reportFile + ".tmp"
	content := fmt.Sprintf("base_url=%s\nsuccess_url=%s\nfailure_url=%s\nport=%d\ncert_file=%s\n",
		srv.BaseURL(), srv.SuccessURL(), srv.FailureURL(), srv.Port(), *certFile)
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		log.Fatalf("heroldfakeunsubscribe: write report: %v", err)
	}
	if err := os.Rename(tmp, *reportFile); err != nil {
		log.Fatalf("heroldfakeunsubscribe: rename report: %v", err)
	}

	log.Printf("heroldfakeunsubscribe: base=%s success=%s failure=%s; report written to %s",
		srv.BaseURL(), srv.SuccessURL(), srv.FailureURL(), *reportFile)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	log.Printf("heroldfakeunsubscribe: shutting down")
}
