package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/hanshuebner/herold/internal/testfakes/fakefcm"
)

func main() {
	var (
		projectID  = flag.String("project-id", "devfake", "Firebase project id embedded in the send path")
		reportFile = flag.String("report-file", "", "path to write key=value report (required)")
	)
	flag.Parse()

	if *reportFile == "" {
		fmt.Fprintln(os.Stderr, "heroldfakefcm: --report-file is required")
		os.Exit(1)
	}

	s, err := fakefcm.NewServer(fakefcm.Options{ProjectID: *projectID})
	if err != nil {
		log.Fatalf("heroldfakefcm: start: %v", err)
	}
	defer s.Close()

	// Write the report file atomically so dev-instance.sh can use
	// wait_for_file to detect readiness without a race.
	tmp := *reportFile + ".tmp"
	content := fmt.Sprintf("http_addr=%s\nsend_url=%s\nproject_id=%s\n",
		s.Addr(), s.SendURL(), *projectID)
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		log.Fatalf("heroldfakefcm: write report: %v", err)
	}
	if err := os.Rename(tmp, *reportFile); err != nil {
		log.Fatalf("heroldfakefcm: rename report: %v", err)
	}

	log.Printf("heroldfakefcm: messages:send at %s; report written to %s", s.SendURL(), *reportFile)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	log.Printf("heroldfakefcm: shutting down")
}
