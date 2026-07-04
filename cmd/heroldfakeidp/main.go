package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/hanshuebner/herold/internal/testfakes/fakeidp"
)

func main() {
	var (
		clientID     = flag.String("client-id", "fakeidp-client", "OAuth client_id the provider expects")
		clientSecret = flag.String("client-secret", "fakeidp-secret", "OAuth client_secret the provider expects")
		reportFile   = flag.String("report-file", "", "path to write key=value report (required)")
	)
	flag.Parse()

	if *reportFile == "" {
		fmt.Fprintln(os.Stderr, "heroldfakeidp: --report-file is required")
		os.Exit(1)
	}

	s := fakeidp.NewServer(fakeidp.Options{
		ClientID:     *clientID,
		ClientSecret: *clientSecret,
	})
	defer s.Close()

	// Write the report file atomically so dev-instance.sh can use
	// wait_for_file to detect readiness without a race.
	tmp := *reportFile + ".tmp"
	content := fmt.Sprintf("auth_url=%s\ntoken_url=%s\nclient_id=%s\nclient_secret=%s\n",
		s.AuthorizeURL(),
		s.TokenURL(),
		s.ClientID(),
		s.ClientSecret(),
	)
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		log.Fatalf("heroldfakeidp: write report: %v", err)
	}
	if err := os.Rename(tmp, *reportFile); err != nil {
		log.Fatalf("heroldfakeidp: rename report: %v", err)
	}

	log.Printf("heroldfakeidp: listening at %s (client_id=%s); report written to %s",
		s.BaseURL(), s.ClientID(), *reportFile)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	log.Printf("heroldfakeidp: shutting down")
}
