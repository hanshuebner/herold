# Profiling herold — operator runbook

*(Created 2026-05-09 after the FTS indexer was observed using
absurd amounts of resident memory with no instrumentation in place
to confirm where the bytes were going. Stop guessing, start measuring.)*

herold ships three diagnostic surfaces for runtime memory and CPU.
Use them when something looks wrong before changing code.

## 1. /debug/pprof/* on the admin listener

Mounted on the admin mux alongside `/metrics`, never on the public
listener. Use the standard `go tool pprof` workflow:

```bash
# Heap snapshot (live in-use objects after GC).
go tool pprof http://<admin-host>/debug/pprof/heap

# Allocation totals (cumulative bytes since process start).
go tool pprof http://<admin-host>/debug/pprof/allocs

# Goroutine dump (counts and stacks; great for leak-detection).
go tool pprof http://<admin-host>/debug/pprof/goroutine

# CPU profile, 30 seconds wall clock.
go tool pprof http://<admin-host>/debug/pprof/profile?seconds=30

# Mutex / block profiles (need MutexProfileFraction / BlockProfileRate
# nudged at process start; not currently set in herold).
go tool pprof http://<admin-host>/debug/pprof/mutex
go tool pprof http://<admin-host>/debug/pprof/block
```

In the pprof CLI: `top10`, `list <funcname>`, `web`, `tree`.

`/debug/pprof/heap` reports **in-use** objects (`-inuse_space`) by
default. To see what *was* allocated even if since freed, use
`go tool pprof -alloc_space http://<host>/debug/pprof/allocs` instead.

## 2. SIGUSR1 heap dump

When the admin listener is unreachable (deadlock, listener wedged,
something else has gone sideways) but the process can still receive
signals:

```bash
kill -USR1 <herold-pid>
```

Writes a heap profile to `$TMPDIR/herold-heap-<UTC-timestamp>.pprof`.
Look in `/var/folders/*/T/` on macOS or `/tmp/` on Linux. Path is
logged at `info` level on the `subsystem=memstats` channel so it is
recoverable from the logs even when stdout is gone.

Then `go tool pprof <path>` from the operator's machine.

## 3. runtime.MemStats in the log stream

Every 60 s (override with `HEROLD_MEMSTATS_INTERVAL_SEC=N`) herold
emits a structured slog line with key MemStats fields:

```
runtime memstats subsystem=memstats alloc=... heap_alloc=...
heap_inuse=... heap_idle=... heap_released=... heap_sys=...
sys=... heap_objects=... num_gc=... pause_total_ns=...
num_goroutine=...
```

This is **always on at info level**. The point is that when memory
spikes are noticed after the fact, the log stream already carries the
trail without anyone needing to have been actively profiling.

Field cheatsheet:
- `alloc` / `heap_alloc`: bytes of allocated, not-yet-freed Go objects.
  This is the closest figure to "what is Go actually using right now".
- `heap_inuse`: bytes in spans that hold at least one live object.
  Higher than `alloc` because of fragmentation.
- `heap_idle`: bytes in spans with no live objects, returned to the
  Go runtime but not yet to the OS.
- `heap_released`: bytes released back to the OS via MADV_FREE
  (darwin) or MADV_DONTNEED (linux). On macOS the OS may keep these
  pages in RSS until it needs them — see below.
- `heap_sys`: total bytes the heap arena has obtained from the OS.
- `sys`: total bytes the entire Go runtime (heap + stack + other)
  has obtained from the OS.
- `num_goroutine`: live goroutine count. Sustained growth here is
  the canonical goroutine-leak signal.

## macOS RSS vs. Go's view

Go uses `MADV_FREE` on darwin. This tells the kernel "these pages are
collectable when you need memory." The kernel honours that request
*lazily*: pages stay in the process's RSS until something else asks
for memory. **A herold process showing 80 GB RSS in Activity Monitor
while `heap_inuse` is 4 GB is normal under MADV_FREE.** The fix is
not in herold; the operating system reclaims the pages on demand.
Linux uses `MADV_DONTNEED` and reflects the release in RSS
immediately.

To check whether a high-RSS observation is real or MADV_FREE noise:

```bash
# Real leak: heap_inuse is high, climbing, and pprof heap shows the
# allocation site.
# MADV_FREE artefact: heap_inuse stays stable, heap_released grows
# as the runtime hands pages back, RSS stays high until the OS
# reclaims.
```

Ask `pprof` first; ask Activity Monitor second.

## When to file an actual fix

Only after the heap profile names a specific allocation site that
holds more bytes than expected, and the source of those bytes is
identifiable in herold's code (not a vendored library's intentional
cache). Before-the-fact "this code looks like it might allocate too
much" reasoning is the trap that produced the 144 GB → 78 GB regression
sequence; trust the profile, not the math.
