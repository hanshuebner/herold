package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/hanshuebner/herold/internal/testfakes/fakeoidc"
)

func main() {
	var (
		clientID     = flag.String("client-id", "fakeoidc-client", "OAuth client_id the provider expects")
		clientSecret = flag.String("client-secret", "fakeoidc-secret", "OAuth client_secret the provider expects")
		reportFile   = flag.String("report-file", "", "path to write key=value report (required)")
	)
	flag.Parse()

	if *reportFile == "" {
		fmt.Fprintln(os.Stderr, "heroldfakeoidc: --report-file is required")
		os.Exit(1)
	}

	s := fakeoidc.NewServer(fakeoidc.Options{
		ClientID:     *clientID,
		ClientSecret: *clientSecret,
	})
	defer s.Close()

	// Write the report file atomically so dev-instance.sh can use
	// wait_for_file to detect readiness without a race.
	tmp := *reportFile + ".tmp"
	content := fmt.Sprintf("issuer_url=%s\nclient_id=%s\nclient_secret=%s\n",
		s.IssuerURL(),
		s.ClientID(),
		s.ClientSecret(),
	)
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		log.Fatalf("heroldfakeoidc: write report: %v", err)
	}
	if err := os.Rename(tmp, *reportFile); err != nil {
		log.Fatalf("heroldfakeoidc: rename report: %v", err)
	}

	log.Printf("heroldfakeoidc: listening at %s (client_id=%s); report written to %s",
		s.BaseURL(), s.ClientID(), *reportFile)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	log.Printf("heroldfakeoidc: shutting down")
}
