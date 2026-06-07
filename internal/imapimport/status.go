package imapimport

// status.go defines WorkerStatus and the per-worker live status handle
// (workerStatus) used to provide Pool.Snapshot() with a concurrency-safe,
// point-in-time view of what each accountWorker is doing right now.
//
// Design (REQ-IMAP-IMP-65):
//   - WorkerStatus is the exported, read-only snapshot type. It is value-typed
//     so callers own their copy; no shared mutable state leaks out.
//   - workerStatus is the mutable, mutex-guarded per-worker state.  All writes
//     go through tiny named helpers (setPhase, setConnMode, …) called at the
//     real transition points in worker.go, sync.go, idle.go, and writeback.go.
//   - reads (snapshot) acquire the same mutex but never block a goroutine for
//     longer than a single field copy.

import (
	"fmt"
	"sync"
	"time"
)

// Phase constants for WorkerStatus.Phase. Kept as untyped string constants so
// they can be compared and printed without a type assertion.
const (
	PhaseStarting   = "starting"
	PhaseConnecting = "connecting"
	PhaseBackoff    = "backoff"
	PhaseSyncing    = "syncing"
	PhaseIdle       = "idle"
	PhasePolling    = "polling"
	PhaseWriteback  = "writeback"
	PhaseStopped    = "stopped"
	PhaseErrored    = "errored"
)

// WorkerStatus is a point-in-time, read-only snapshot of what one
// accountWorker is doing right now. Returned (by value) from Pool.Snapshot.
// All fields are safe to read without additional synchronisation because each
// Snapshot call returns a fresh copy.
//
// REQ-IMAP-IMP-65.
type WorkerStatus struct {
	// AccountID is the stable ID of the IMAPImportAccount row.
	AccountID string
	// PrincipalID is the owning principal's ID.
	PrincipalID string
	// AccountName is the operator-visible label for this account.
	AccountName string
	// Host is the upstream IMAP server hostname.
	Host string

	// Phase is the current lifecycle phase. One of the Phase* constants:
	// "starting", "connecting", "backoff", "syncing", "idle", "polling",
	// "writeback", "stopped", "errored".
	Phase string
	// CurrentFolder is the upstream folder being synced right now.
	// Empty when Phase != "syncing".
	CurrentFolder string
	// ConnMode describes the connection topology decided for this session:
	// "single" (one connection shared by IDLE and FETCH), "dual" (primary
	// for IDLE, second for FETCH+writeback), or "" (not yet decided).
	ConnMode string

	// Connected is true when at least one authenticated connection to the
	// upstream is open.
	Connected bool

	// PhaseSince is the wall-clock time at which the current Phase began.
	PhaseSince time.Time
	// LastSyncAt is the time of the last successful syncAllFolders round,
	// or nil if no successful sync has occurred this run.
	LastSyncAt *time.Time
	// ConsecutiveFailures is the number of consecutive failed connection
	// attempts. Reset to 0 after a successful connection.
	ConsecutiveFailures int
	// NextPollAt is the scheduled time for the next NOOP-poll tick when
	// Phase == "polling". Nil otherwise.
	NextPollAt *time.Time

	// MessagesFetched is the number of messages fetched and ingested into
	// herold this run (cumulative since the worker started).
	MessagesFetched int64
	// FlagsPropagated is the number of flag changes pushed upstream this run
	// (cumulative, up direction only — REQ-IMAP-IMP-63).
	FlagsPropagated int64

	// LastError is the redacted last error string. Never contains credential
	// material (REQ-IMAP-IMP-71).
	LastError string
}

// String returns a one-line human-readable summary of the status. Useful for
// the "herold imapimport status" CLI (REQ-IMAP-IMP-62) and log lines.
func (s WorkerStatus) String() string {
	conn := "disconnected"
	if s.Connected {
		conn = "connected"
	}
	extra := ""
	switch s.Phase {
	case PhaseSyncing:
		if s.CurrentFolder != "" {
			extra = " folder=" + s.CurrentFolder
		}
	case PhaseBackoff, PhaseErrored:
		if s.ConsecutiveFailures > 0 {
			extra = fmt.Sprintf(" failures=%d", s.ConsecutiveFailures)
		}
	case PhasePolling:
		if s.NextPollAt != nil {
			extra = fmt.Sprintf(" next=%s", s.NextPollAt.Format(time.RFC3339))
		}
	}
	return fmt.Sprintf("account=%s host=%s phase=%s %s%s fetched=%d propagated=%d",
		s.AccountID, s.Host, s.Phase, conn, extra, s.MessagesFetched, s.FlagsPropagated)
}

// workerStatus is the mutable per-accountWorker status. It is embedded in
// accountWorker and mutated only through the named helpers below, which all
// hold mu. Snapshot copies the fields out under mu. The mutex is never held
// across a blocking I/O call, so reads (Snapshot) never block workers.
type workerStatus struct {
	mu sync.Mutex

	accountID   string
	principalID string
	accountName string
	host        string

	phase         string
	currentFolder string
	connMode      string
	connected     bool
	phaseSince    time.Time
	lastSyncAt    *time.Time
	consFailures  int
	nextPollAt    *time.Time
	msgFetched    int64
	flagPropag    int64
	lastError     string
}

// snapshot returns a copy of the current status. The caller owns the copy.
func (ws *workerStatus) snapshot() WorkerStatus {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	s := WorkerStatus{
		AccountID:           ws.accountID,
		PrincipalID:         ws.principalID,
		AccountName:         ws.accountName,
		Host:                ws.host,
		Phase:               ws.phase,
		CurrentFolder:       ws.currentFolder,
		ConnMode:            ws.connMode,
		Connected:           ws.connected,
		PhaseSince:          ws.phaseSince,
		ConsecutiveFailures: ws.consFailures,
		MessagesFetched:     ws.msgFetched,
		FlagsPropagated:     ws.flagPropag,
		LastError:           ws.lastError,
	}
	if ws.lastSyncAt != nil {
		t := *ws.lastSyncAt
		s.LastSyncAt = &t
	}
	if ws.nextPollAt != nil {
		t := *ws.nextPollAt
		s.NextPollAt = &t
	}
	return s
}

// setPhase transitions to the named phase at the given clock time.
// CurrentFolder is reset to "" on any phase that is not "syncing".
func (ws *workerStatus) setPhase(phase string, now time.Time) {
	ws.mu.Lock()
	ws.phase = phase
	ws.phaseSince = now
	if phase != PhaseSyncing {
		ws.currentFolder = ""
	}
	if phase != PhasePolling {
		ws.nextPollAt = nil
	}
	ws.mu.Unlock()
}

// setSyncingFolder records which folder is currently being synced.
// This is called after setPhase(PhaseSyncing, …) to set the folder name.
func (ws *workerStatus) setSyncingFolder(folder string) {
	ws.mu.Lock()
	ws.currentFolder = folder
	ws.mu.Unlock()
}

// setConnMode records "single" or "dual" once the IDLE-loop has decided.
func (ws *workerStatus) setConnMode(mode string) {
	ws.mu.Lock()
	ws.connMode = mode
	ws.mu.Unlock()
}

// setConnected records whether at least one upstream connection is open.
func (ws *workerStatus) setConnected(connected bool) {
	ws.mu.Lock()
	ws.connected = connected
	if !connected {
		ws.connMode = ""
	}
	ws.mu.Unlock()
}

// setConsecutiveFailures records the number of consecutive failed attempts
// and clears lastError when reset to 0.
func (ws *workerStatus) setConsecutiveFailures(n int) {
	ws.mu.Lock()
	ws.consFailures = n
	ws.mu.Unlock()
}

// setLastError stores a redacted error string.
func (ws *workerStatus) setLastError(msg string) {
	ws.mu.Lock()
	ws.lastError = msg
	ws.mu.Unlock()
}

// recordSyncOK records that a successful syncAllFolders round completed at now.
func (ws *workerStatus) recordSyncOK(now time.Time) {
	ws.mu.Lock()
	t := now
	ws.lastSyncAt = &t
	ws.mu.Unlock()
}

// setNextPoll records when the next NOOP-poll tick is scheduled.
func (ws *workerStatus) setNextPoll(t time.Time) {
	ws.mu.Lock()
	ws.nextPollAt = &t
	ws.mu.Unlock()
}

// incFetched adds n to the cumulative messages-fetched counter.
func (ws *workerStatus) incFetched(n int64) {
	if n == 0 {
		return
	}
	ws.mu.Lock()
	ws.msgFetched += n
	ws.mu.Unlock()
}

// incPropagated adds n to the cumulative flags-propagated counter.
func (ws *workerStatus) incPropagated(n int64) {
	if n == 0 {
		return
	}
	ws.mu.Lock()
	ws.flagPropag += n
	ws.mu.Unlock()
}
