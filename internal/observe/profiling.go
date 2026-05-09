package observe

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	httppprof "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	runtimepprof "runtime/pprof"
	"time"
)

// PprofMux mirrors the side-effect registration that net/http/pprof
// performs against http.DefaultServeMux, but writes onto the supplied
// mux instead. Mount this on the admin listener — never on a public
// listener — so operators can fetch live heap / goroutine / cpu /
// allocs profiles via the standard `go tool pprof` URLs:
//
//	go tool pprof http://<admin-host>/debug/pprof/heap
//	go tool pprof http://<admin-host>/debug/pprof/goroutine
//	go tool pprof http://<admin-host>/debug/pprof/allocs
//	go tool pprof http://<admin-host>/debug/pprof/profile?seconds=30
//
// The pprof endpoints have no auth of their own; they inherit whatever
// auth wraps the admin mux. Mount only on listeners that are not
// reachable from the internet.
func PprofMux(mux *http.ServeMux) {
	// Local rebinding so the registrations match the
	// net/http/pprof.init() pattern exactly. mux is parameterised so
	// the caller can pick the admin mux without polluting
	// http.DefaultServeMux.
	mux.HandleFunc("/debug/pprof/", httppprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", httppprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", httppprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", httppprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", httppprof.Trace)
}

// StartMemStatsLogger spawns a goroutine that emits a structured
// runtime.MemStats summary at logger.Info every interval. It returns
// once ctx is cancelled. Default interval (when interval <= 0) is
// 60 seconds, which is quiet enough to leave on permanently and dense
// enough to catch a memory event in flight.
//
// Why this exists: pprof gives a live snapshot but only when the
// operator is actively grabbing it. When the herold process is observed
// to be using N gigabytes of resident memory hours into a run, there is
// no historical record of how it got there. The MemStats line gives
// every Info-level scrape a permanent, parseable trail.
//
// Particularly important on macOS: Go uses MADV_FREE on darwin, which
// returns memory to the OS lazily (kernel reclaims when it needs to).
// HeapReleased reports bytes Go has handed back; RSS as reported by
// Activity Monitor or `ps` can stay elevated until the kernel actually
// reclaims those pages. Comparing HeapInuse (live + retained heap) to
// the RSS the OS sees is the canonical way to tell a real leak from a
// macOS-RSS-accounting artefact.
func StartMemStatsLogger(ctx context.Context, logger *slog.Logger, interval time.Duration) {
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = 60 * time.Second
	}
	go func() {
		// Emit once at startup so the log has an early baseline; then
		// tick.
		emitMemStats(ctx, logger)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				emitMemStats(ctx, logger)
			}
		}
	}()
}

func emitMemStats(ctx context.Context, logger *slog.Logger) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	logger.LogAttrs(ctx, slog.LevelInfo, "runtime memstats",
		slog.String("subsystem", "memstats"),
		slog.Uint64("alloc", ms.Alloc),
		slog.Uint64("total_alloc", ms.TotalAlloc),
		slog.Uint64("sys", ms.Sys),
		slog.Uint64("heap_alloc", ms.HeapAlloc),
		slog.Uint64("heap_sys", ms.HeapSys),
		slog.Uint64("heap_idle", ms.HeapIdle),
		slog.Uint64("heap_inuse", ms.HeapInuse),
		slog.Uint64("heap_released", ms.HeapReleased),
		slog.Uint64("heap_objects", ms.HeapObjects),
		slog.Uint64("stack_inuse", ms.StackInuse),
		slog.Uint64("stack_sys", ms.StackSys),
		slog.Uint64("next_gc", ms.NextGC),
		slog.Uint64("num_gc", uint64(ms.NumGC)),
		slog.Uint64("pause_total_ns", ms.PauseTotalNs),
		slog.Int("num_goroutine", runtime.NumGoroutine()),
	)
}

// StartHeapDumpOnSignal registers sig as a trigger that writes a heap
// profile to dir/herold-heap-<UTC-timestamp>.pprof. Returns once ctx is
// cancelled. Useful when the admin /debug/pprof/heap endpoint is
// unreachable (deadlock, listener wedged, network gone) but the process
// can still receive signals.
//
// On macOS and Linux SIGUSR1 is the conventional pick — it has no
// kernel-defined behaviour and herold's other signal handlers
// (SIGHUP for reload, SIGINT/SIGTERM for shutdown) leave it free.
func StartHeapDumpOnSignal(ctx context.Context, logger *slog.Logger, dir string, sig os.Signal) {
	if logger == nil {
		logger = slog.Default()
	}
	if dir == "" {
		dir = os.TempDir()
	}
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, sig)
		defer signal.Stop(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				path := filepath.Join(dir, fmt.Sprintf("herold-heap-%s.pprof",
					time.Now().UTC().Format("20060102T150405Z")))
				if err := writeHeapDump(path); err != nil {
					logger.LogAttrs(ctx, slog.LevelWarn, "heap dump on signal failed",
						slog.String("subsystem", "memstats"),
						slog.String("path", path),
						slog.String("err", err.Error()))
					continue
				}
				logger.LogAttrs(ctx, slog.LevelInfo, "heap dump on signal written",
					slog.String("subsystem", "memstats"),
					slog.String("path", path))
			}
		}
	}()
}

func writeHeapDump(path string) error {
	// Run a GC first so the profile reflects what is genuinely live, not
	// what is allocated-but-collectible. The pprof heap profile reports
	// in-use after the most recent collection; without the explicit GC
	// the report can over-attribute by an entire allocation cycle.
	runtime.GC()
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create heap dump: %w", err)
	}
	defer f.Close()
	if err := runtimepprof.Lookup("heap").WriteTo(f, 0); err != nil {
		return fmt.Errorf("write heap profile: %w", err)
	}
	return nil
}
