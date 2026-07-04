package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/hanshuebner/herold/internal/testfakes/fakesmtp"
)

func main() {
	var (
		hostname   = flag.String("hostname", "smtp.fake.test", "hostname announced in the SMTP greeting")
		reportFile = flag.String("report-file", "", "path to write key=value report (required)")
	)
	flag.Parse()

	if *reportFile == "" {
		fmt.Fprintln(os.Stderr, "heroldfakesmtp: --report-file is required")
		os.Exit(1)
	}

	smtp, err := fakesmtp.NewServer(fakesmtp.Options{
		Security: fakesmtp.Plain,
		Hostname: *hostname,
	})
	if err != nil {
		log.Fatalf("heroldfakesmtp: start SMTP: %v", err)
	}
	defer smtp.Close()

	// HTTP status server on a separate kernel-picked port.
	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("heroldfakesmtp: start HTTP: %v", err)
	}
	httpSrv := &http.Server{Handler: smtp.HTTPHandler()}
	go func() {
		if err := httpSrv.Serve(httpLn); err != nil && err != http.ErrServerClosed {
			log.Printf("heroldfakesmtp: HTTP server error: %v", err)
		}
	}()

	httpAddr := httpLn.Addr().String()
	smtpAddr := smtp.Addr()

	// Write the report file atomically so dev-instance.sh can use
	// wait_for_file to detect readiness without a race.
	tmp := *reportFile + ".tmp"
	content := fmt.Sprintf("smtp_addr=%s\nhttp_addr=%s\n", smtpAddr, httpAddr)
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		log.Fatalf("heroldfakesmtp: write report: %v", err)
	}
	if err := os.Rename(tmp, *reportFile); err != nil {
		log.Fatalf("heroldfakesmtp: rename report: %v", err)
	}

	log.Printf("heroldfakesmtp: SMTP at %s  HTTP status at http://%s; report written to %s",
		smtpAddr, httpAddr, *reportFile)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	log.Printf("heroldfakesmtp: shutting down")
	_ = httpSrv.Close()
}
