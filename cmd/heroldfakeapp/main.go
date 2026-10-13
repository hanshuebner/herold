// Command heroldfakeapp runs a standalone fakeapp server for
// scripts/dev-instance.sh (REQ-APP-70, REQ-APP-71). It writes a
// key=value report file (origin, port, app_key) so the shell script can
// list the origin in [server.apps] allowed_insecure_origins, register
// the app, and hand the fake its signing secret via PUT /secret.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hanshuebner/herold/internal/testfakes/fakeapp"
)

func main() {
	var (
		reportFile = flag.String("report-file", "", "path to write key=value report (required)")
		listen     = flag.String("listen", "127.0.0.1:0", "listen address")
		secret     = flag.String("secret", "", "initial signing secret (normally set later via PUT /secret)")
		slowDelay  = flag.Duration("slow-delay", 30*time.Second, "how long the slow extension waits before answering")
	)
	flag.Parse()
	if *reportFile == "" {
		fmt.Fprintln(os.Stderr, "heroldfakeapp: --report-file is required")
		os.Exit(1)
	}

	srv, err := fakeapp.NewServer(fakeapp.Options{Addr: *listen, Secret: *secret, SlowDelay: *slowDelay})
	if err != nil {
		log.Fatalf("heroldfakeapp: start: %v", err)
	}
	defer srv.Close()

	// Write the report atomically so dev-instance.sh can wait for the
	// file without a race.
	tmp := *reportFile + ".tmp"
	content := fmt.Sprintf("origin=%s\nport=%d\napp_key=%s\n", srv.Origin(), srv.Port(), srv.AppKey())
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		log.Fatalf("heroldfakeapp: write report: %v", err)
	}
	if err := os.Rename(tmp, *reportFile); err != nil {
		log.Fatalf("heroldfakeapp: rename report: %v", err)
	}
	log.Printf("heroldfakeapp: origin=%s app_key=%s; report written to %s", srv.Origin(), srv.AppKey(), *reportFile)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	log.Printf("heroldfakeapp: shutting down")
}
