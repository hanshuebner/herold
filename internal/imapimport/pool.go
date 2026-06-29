package imapimport

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// poolPollInterval is how often Run re-queries the store for newly-enabled
// accounts. Long-running workers are not affected: they persist across ticks.
// 10 s gives prompt startup for accounts created after the pool launches
// without meaningful extra load on the store.
const poolPollInterval = 10 * time.Second

// Pool is the per-instance supervisor for IMAP import workers. It starts
// one accountWorker goroutine per enabled IMAPImportAccount, bounded by
// cfg.ConcurrentAccounts. Clean shutdown is triggered by cancelling the
// context passed to Run (REQ-IMAP-IMP-26).
//
// Construct with NewPool; call Run to start.
type Pool struct {
	st           store.Store
	dataKey      []byte
	categoriser  Categoriser
	cfg          sysconfig.IMAPImportConfig
	log          *slog.Logger
	clk          clock.Clock
	dialer       Dialer
	pollInterval time.Duration

	// registry guards the live worker map. Workers register on launch and
	// deregister on return. Snapshot reads it under the same mutex so that
	// reads never block a running worker for more than the time needed to
	// copy a single pointer. REQ-IMAP-IMP-65.
	registryMu sync.Mutex
	registry   map[string]*accountWorker // keyed by accountID
}

// PoolOptions carries the constructor parameters for Pool. All fields
// except Categoriser and Dialer are required.
type PoolOptions struct {
	// Store is the herold metadata/blob/FTS store.
	Store store.Store
	// DataKey is the AEAD data key for secrets.Open. It is loaded once
	// at boot via secrets.LoadDataKey and passed in as a dependency.
	// The Pool never loads or stores the key — it is only forwarded to
	// accountWorker instances.
	DataKey []byte
	// Categoriser is the optional LLM categorisation seam (3c). Pass
	// nil to use a no-op categoriser.
	Categoriser Categoriser
	// Config is the resolved [imap_import] sysconfig block.
	Config sysconfig.IMAPImportConfig
	// Logger is the base logger. The pool adds activity and account
	// labels to each child logger (REQ-IMAP-IMP-64).
	Logger *slog.Logger
	// Clock is the clock used for backoff timing. Tests inject a
	// FakeClock so backoff sleeps are deterministic and instantaneous.
	Clock clock.Clock
	// Dialer is the IMAP connection factory. Pass nil to use the
	// production dialer. Tests inject a fakeDialer.
	Dialer Dialer
	// PollInterval overrides poolPollInterval. Zero uses the default.
	// Tests inject a short interval so the dynamic account pickup path
	// can be exercised without waiting 10 s.
	PollInterval time.Duration
}

// NewPool constructs a Pool from opts. Run has not been called yet;
// no goroutines are started.
func NewPool(opts PoolOptions) *Pool {
	observe.RegisterIMAPImportMetrics()

	cat := opts.Categoriser
	if cat == nil {
		cat = noopCategoriser{}
	}
	d := opts.Dialer
	if d == nil {
		d = newProductionDialer(opts.Config)
	}
	poll := opts.PollInterval
	if poll <= 0 {
		poll = poolPollInterval
	}
	return &Pool{
		st:           opts.Store,
		dataKey:      opts.DataKey,
		categoriser:  cat,
		cfg:          opts.Config,
		log:          opts.Logger.With(slog.String("activity", "imap-import")),
		clk:          opts.Clock,
		dialer:       d,
		pollInterval: poll,
		registry:     make(map[string]*accountWorker),
	}
}

// Run supervises IMAP import workers for every enabled account. It scans the
// store at startup and then every poolPollInterval, launching one
// accountWorker goroutine per enabled account that does not already have a
// running worker. The pool is bounded by the ConcurrentAccounts semaphore.
//
// Run blocks until ctx is cancelled, then waits for all workers to return
// before returning nil. Only returns a non-nil error on a fatal setup failure.
//
// REQ-IMAP-IMP-26.
func (p *Pool) Run(ctx context.Context) error {
	// Already cancelled before we started.
	if ctx.Err() != nil {
		return nil
	}

	concurrency := p.cfg.ConcurrentAccounts
	if concurrency < 1 {
		concurrency = 16
	}

	// sem bounds the number of concurrently running workers across all
	// poll ticks.
	sem := make(chan struct{}, concurrency)

	var wg sync.WaitGroup

	// startWorker launches a worker for acct when no worker is already in
	// the registry. It skips the launch if the semaphore is at capacity;
	// the next poll tick will retry.
	startWorker := func(acct store.IMAPImportAccount) {
		p.registryMu.Lock()
		_, running := p.registry[acct.ID]
		p.registryMu.Unlock()
		if running {
			return
		}
		// Non-blocking acquire: skip if all slots are taken; the next tick
		// retries. This avoids blocking the poll loop inside the ticker case.
		select {
		case sem <- struct{}{}:
		default:
			return
		}
		w := newAccountWorker(accountWorkerOpts{
			account:     acct,
			store:       p.st,
			dataKey:     p.dataKey,
			categoriser: p.categoriser,
			cfg:         p.cfg,
			log: p.log.With(
				slog.String("account_id", acct.ID),
				slog.Any("principal_id", acct.PrincipalID),
			),
			clk:    p.clk,
			dialer: p.dialer,
		})
		wg.Add(1)
		// Register before launching so Snapshot sees the worker immediately.
		p.registryMu.Lock()
		p.registry[acct.ID] = w
		p.registryMu.Unlock()
		go func(w *accountWorker, accountID string) {
			defer func() {
				// Drop-out on return: a stopped worker disappears from Snapshot.
				p.registryMu.Lock()
				delete(p.registry, accountID)
				p.registryMu.Unlock()
				<-sem // release
				wg.Done()
			}()
			w.run(ctx)
		}(w, acct.ID)
	}

	// scan queries enabled accounts and starts workers for any that are not
	// already running.
	scan := func() {
		accounts, err := p.st.Meta().ListEnabledIMAPImportAccounts(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			p.log.Warn("imapimport: error listing accounts",
				slog.String("error", err.Error()))
			return
		}
		for _, acct := range accounts {
			if ctx.Err() != nil {
				return
			}
			startWorker(acct)
		}
	}

	// Initial scan: start workers for any accounts that exist at boot.
	scan()

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Drain all workers before returning.
			wg.Wait()
			return nil
		case <-ticker.C:
			scan()
		}
	}
}

// Snapshot returns a point-in-time copy of every live worker's status,
// sorted by AccountID. It is a pure in-memory read: no store calls, no
// network I/O. Each call acquires the registry mutex briefly to copy the
// worker pointer slice, then acquires each worker's per-field mutex to copy
// the status fields — neither lock is held for more than a field copy, so
// workers are never blocked. REQ-IMAP-IMP-65.
func (p *Pool) Snapshot() []WorkerStatus {
	// Collect live worker pointers under the registry lock.
	p.registryMu.Lock()
	workers := make([]*accountWorker, 0, len(p.registry))
	for _, w := range p.registry {
		workers = append(workers, w)
	}
	p.registryMu.Unlock()

	// Copy each worker's status without holding the registry lock.
	out := make([]WorkerStatus, 0, len(workers))
	for _, w := range workers {
		out = append(out, w.status.snapshot())
	}

	// Sort by AccountID for deterministic output.
	sort.Slice(out, func(i, j int) bool {
		return out[i].AccountID < out[j].AccountID
	})
	return out
}
