package imapimport

import (
	"context"
	"log/slog"
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
		go func(w *accountWorker) {
			defer func() {
				<-sem // release
				wg.Done()
			}()
			w.run(ctx)
		}(w)
	}
	wg.Wait()
	return nil
}
