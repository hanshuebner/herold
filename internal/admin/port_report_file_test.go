package admin

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/hanshuebner/herold/internal/sysconfig"
)

// portReportEntry is the [[listener]] shape written to the port report file.
type portReportEntry struct {
	Name    string `toml:"name"`
	Address string `toml:"address"`
}

// portReportFile is the top-level TOML shape of the port report file.
type portReportFile struct {
	Listener []portReportEntry `toml:"listener"`
}

// TestPortReportFile_BootWritesAndShutdownRemoves boots a minimal server with
// two HTTP listeners on port 0 and port_report_file set, then:
//  1. Waits for the file to appear (it is written before the ready signal).
//  2. Parses and asserts both listener entries are present with non-zero ports.
//  3. Cancels the server and asserts the file is removed on graceful shutdown.
func TestPortReportFile_BootWritesAndShutdownRemoves(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := generateSelfSignedCert(t, dir, []string{"localhost"})
	reportPath := filepath.Join(dir, "ports.toml")

	tomlContent := fmt.Sprintf(`
[server]
hostname = "test.local"
data_dir = %q
run_as_user = ""
run_as_group = ""
port_report_file = %q

[server.admin_tls]
source = "file"
cert_file = %q
key_file = %q

[server.storage]
backend = "sqlite"
[server.storage.sqlite]
path = %q

[[listener]]
name = "public"
address = "127.0.0.1:0"
protocol = "admin"
kind = "public"
tls = "none"

[[listener]]
name = "admin"
address = "127.0.0.1:0"
protocol = "admin"
kind = "admin"
tls = "none"

[observability]
log_format = "text"
log_level = "warn"
metrics_bind = ""
`, dir, reportPath, certPath, keyPath, filepath.Join(dir, "db.sqlite"))

	systomlPath := filepath.Join(dir, "system.toml")
	if err := os.WriteFile(systomlPath, []byte(tomlContent), 0o600); err != nil {
		t.Fatalf("write system.toml: %v", err)
	}
	cfg, err := sysconfig.Load(systomlPath)
	if err != nil {
		t.Fatalf("sysconfig.Load: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan struct{})
	addrs := make(map[string]string)
	addrsMu := &sync.Mutex{}

	go func() {
		defer close(done)
		if err := StartServer(ctx, cfg, StartOpts{
			Logger:           slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
			Ready:            ready,
			ListenerAddrs:    addrs,
			ListenerAddrsMu:  addrsMu,
			ExternalShutdown: true,
		}); err != nil {
			t.Logf("StartServer exited: %v", err)
		}
	}()

	// Wait for ready (the port report file is written before ready).
	select {
	case <-ready:
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatalf("server did not become ready within timeout")
	}

	// The port report file must exist.
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		cancel()
		<-done
		t.Fatalf("port report file not found after ready: %v", err)
	}

	var report portReportFile
	if err := toml.Unmarshal(raw, &report); err != nil {
		cancel()
		<-done
		t.Fatalf("port report file parse error: %v\ncontent:\n%s", err, string(raw))
	}

	// Must have exactly 2 entries (one per listener).
	if len(report.Listener) != 2 {
		cancel()
		<-done
		t.Fatalf("expected 2 listener entries in port report; got %d\ncontent:\n%s", len(report.Listener), string(raw))
	}

	// Build a name→address map for assertion.
	byName := make(map[string]string, len(report.Listener))
	for _, e := range report.Listener {
		byName[e.Name] = e.Address
	}

	for _, name := range []string{"public", "admin"} {
		addr, ok := byName[name]
		if !ok {
			t.Errorf("listener %q missing from port report; got: %+v", name, byName)
			continue
		}
		// The address must be parseable and have a non-zero port.
		idx := strings.LastIndex(addr, ":")
		if idx < 0 {
			t.Errorf("listener %q: address %q has no colon", name, addr)
			continue
		}
		portStr := addr[idx+1:]
		port, parseErr := strconv.Atoi(portStr)
		if parseErr != nil {
			t.Errorf("listener %q: port %q is not an integer: %v", name, portStr, parseErr)
			continue
		}
		if port == 0 {
			t.Errorf("listener %q: kernel should have replaced port 0; got address %q", name, addr)
		}
	}

	// Trigger graceful shutdown.
	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("server did not shut down within grace window")
	}

	// After shutdown the file must be gone.
	if _, statErr := os.Stat(reportPath); !os.IsNotExist(statErr) {
		t.Errorf("port report file should have been removed on shutdown; stat=%v", statErr)
	}
}
