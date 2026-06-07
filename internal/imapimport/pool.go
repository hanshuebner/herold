package imapimport

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/observe"
	"github.com/hanshuebner/herold/internal/store"
	"github.com/hanshuebner/herold/internal/sysconfig"
)

// Pool is the per-instance supervisor for IMAP import workers. It starts
// one accountWorker goroutine per enabled IMAPImportAccount, bounded by
// cfg.ConcurrentAccounts. Clean shutdown is triggered by cancelling the
// context passed to Run (REQ-IMAP-IMP-26).
//
// Construct with NewPool; call Run to start.
type Pool struct {
	st          store.Store
	dataKey     []byte
	categoriser Categoriser
	cfg         sysconfig.IMAPImportConfig
	log         *slog.Logger
	clk         clock.Clock
	dialer      Dialer

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
	return &Pool{
		st:          opts.Store,
		dataKey:     opts.DataKey,
		categoriser: cat,
		cfg:         opts.Config,
		log:         opts.Logger.With(slog.String("activity", "imap-import")),
		clk:         opts.Clock,
		dialer:      d,
		registry:    make(map[string]*accountWorker),
	}
}

// Run starts one accountWorker per enabled account, bounded by the
// ConcurrentAccounts semaphore. It blocks until ctx is cancelled and
// all workers have returned. Safe to call from one goroutine only.
// Returns nil on clean shutdown (ctx cancel). Only returns a non-nil
// error when a fatal setup failure occurs.
//
// REQ-IMAP-IMP-26.
func (p *Pool) Run(ctx context.Context) error {
	// Already cancelled before we started.
	if ctx.Err() != nil {
		return nil
	}

	accounts, err := p.st.Meta().ListEnabledIMAPImportAccounts(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if len(accounts) == 0 {
		p.log.Info("imapimport: pool started with no enabled accounts; waiting for context cancel")
		<-ctx.Done()
		return nil
	}

	concurrency := p.cfg.ConcurrentAccounts
	if concurrency < 1 {
		concurrency = 16
	}

	// sem bounds the number of concurrently running workers.
	sem := make(chan struct{}, concurrency)

	var wg sync.WaitGroup
launch:
	for _, acct := range accounts {
		select {
		case <-ctx.Done():
			break launch
		default:
		}
		sem <- struct{}{} // acquire
		wg.Add(1)
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
	wg.Wait()
	return nil
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
