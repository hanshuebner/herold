package maildmarc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanshuebner/herold/internal/clock"
	"github.com/hanshuebner/herold/internal/cursors"
	"github.com/hanshuebner/herold/internal/store"
)

// DefaultIntakeCursorKey is the row key the intake worker uses in the
// shared cursors table to persist its resume Seq. Distinct from the
// FTS / webhook keys so the three workers advance independently.
const DefaultIntakeCursorKey = "dmarc-intake"

// DefaultIntakePollInterval is the change-feed poll cadence. DMARC
// reports are not latency-critical so we poll once a minute by default;
// operators can tighten the interval via IntakeOptions if their
// dashboards need fresher data.
const DefaultIntakePollInterval = 60 * time.Second

// DefaultIntakeBatchSize bounds the per-tick read.
const DefaultIntakeBatchSize = 64

// IntakeOptions configures the change-feed-driven DMARC intake worker.
type IntakeOptions struct {
	// PollInterval is how long the loop sleeps when the change feed
	// returns no new entries. <= 0 uses DefaultIntakePollInterval.
	PollInterval time.Duration

	// RecipientPattern is a case-insensitive prefix the inbound
	// envelope/recipient must match before the message is considered.
	// Operators usually set the published rua=mailto: target's local
	// part plus '@', e.g. "dmarc-reports@". Empty means "match every
	// inbound message" — mainly useful for tests.
	RecipientPattern string

	// CursorKey overrides DefaultIntakeCursorKey. Tests use this to
	// isolate concurrent runs against the same database.
	CursorKey string

	// BatchSize bounds the per-tick read from the change feed.
	// <= 0 uses DefaultIntakeBatchSize.
	BatchSize int
}

// Intake watches the global change feed for new EntityKindEmail rows,
// pulls each matching message body from the blob store, and feeds it
// through Ingestor.IngestMessage.
//
// The worker is the canonical Phase 2 change-feed consumer pattern,
// matching internal/protowebhook/dispatcher.go: read with
// ReadChangeFeedForFTS, advance a cursor in the cursors table, swallow
// non-fatal errors (log + advance) so a single bad message never stalls
// the whole feed.
type Intake struct {
	meta     store.Metadata
	blobs    store.Blobs
	ingestor *Ingestor
	logger   *slog.Logger
	clock    clock.Clock
	opts     IntakeOptions

	cursor  atomic.Uint64
	running atomic.Bool

	// fts is the change-feed surface. ReadChangeFeedForFTS lives on the
	// FTS sub-interface so the worker holds a typed reference to the
	// composite store for that one call.
	store store.Store
}

// NewIntake returns an Intake. ingestor MUST be non-nil; the caller is
// responsible for keeping the underlying store alive for the lifetime
// of the Intake.
func NewIntake(s store.Store, ingestor *Ingestor, logger *slog.Logger, clk clock.Clock, opts IntakeOptions) *Intake {
	if s == nil {
		panic("maildmarc: NewIntake with nil store.Store")
	}
	if ingestor == nil {
		panic("maildmarc: NewIntake with nil Ingestor")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.NewReal()
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultIntakePollInterval
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultIntakeBatchSize
	}
	if opts.CursorKey == "" {
		opts.CursorKey = DefaultIntakeCursorKey
	}
	opts.RecipientPattern = strings.ToLower(strings.TrimSpace(opts.RecipientPattern))

	return &Intake{
		meta:     s.Meta(),
		blobs:    s.Blobs(),
		ingestor: ingestor,
		logger:   logger,
		clock:    clk,
		opts:     opts,
		store:    s,
	}
}

// Cursor returns the worker's last persisted resume Seq.
func (i *Intake) Cursor() uint64 { return i.cursor.Load() }

// Run consumes the change feed until ctx is cancelled. Returns nil on
// graceful shutdown; non-nil only on a fatal cursor-table read failure
// (the worker treats per-message lookup errors as advisory and
// advances). Safe to call once per Intake.
func (i *Intake) Run(ctx context.Context) error {
	if !i.running.CompareAndSwap(false, true) {
		return errors.New("maildmarc: Intake already running")
	}
	defer i.running.Store(false)
	// Final flush on shutdown so the in-memory cursor reflects on
	// disk even when the in-loop SetFTSCursor lost its race with
	// ctx cancellation. Uses a fresh, short-deadline ctx so the
	// flush itself is not cancelled the moment it starts.
	defer cursors.ShutdownFlusher{
		Get:       i.cursor.Load,
		Put:       func(ctx context.Context, seq uint64) error { return i.meta.SetFTSCursor(ctx, i.opts.CursorKey, seq) },
		Logger:    i.logger,
		Subsystem: "maildmarc",
	}.Flush()

	if seq, err := i.meta.GetFTSCursor(ctx, i.opts.CursorKey); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return fmt.Errorf("maildmarc: load intake cursor: %w", err)
	} else {
		i.cursor.Store(seq)
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		changes, err := i.store.FTS().ReadChangeFeedForFTS(ctx, i.cursor.Load(), i.opts.BatchSize)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return fmt.Errorf("maildmarc: read change feed: %w", err)
		}
		if len(changes) == 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-i.clock.After(i.opts.PollInterval):
			}
			continue
		}
		var maxSeq uint64
		for _, c := range changes {
			if err := ctx.Err(); err != nil {
				return nil
			}
			if c.Seq > maxSeq {
				maxSeq = c.Seq
			}
			if c.Kind != store.EntityKindEmail || c.Op != store.ChangeOpCreated {
				continue
			}
			i.processChange(ctx, c)
		}
		if maxSeq > 0 {
			i.cursor.Store(maxSeq)
			if err := i.meta.SetFTSCursor(ctx, i.opts.CursorKey, maxSeq); err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil
				}
				i.logger.WarnContext(ctx, "maildmarc: persist intake cursor",
					slog.String("activity", "internal"),
					slog.String("subsystem", "maildmarc"),
					slog.String("worker", "ingest"),
					slog.String("key", i.opts.CursorKey),
					slog.Uint64("seq", maxSeq),
					slog.Any("err", err),
				)
			}
		}
	}
}

// processChange fetches the message indicated by c, gates on the
// recipient pattern, and dispatches to the Ingestor. Errors are logged
// at warn level and absorbed so the cursor still advances.
func (i *Intake) processChange(ctx context.Context, c store.FTSChange) {
	msgID := store.MessageID(c.EntityID)
	msg, err := i.meta.GetMessage(ctx, msgID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			i.logger.WarnContext(ctx, "maildmarc: get message",
				slog.String("activity", "internal"),
				slog.String("subsystem", "maildmarc"),
				slog.String("worker", "ingest"),
				slog.Uint64("message_id", uint64(msgID)),
				slog.Any("err", err))
		}
		return
	}
	if !i.recipientMatches(msg) {
		return
	}
	body, err := i.readBody(ctx, msg.Blob.Hash)
	if err != nil {
		i.logger.WarnContext(ctx, "maildmarc: read body",
			slog.String("activity", "internal"),
			slog.String("subsystem", "maildmarc"),
			slog.String("worker", "ingest"),
			slog.Uint64("message_id", uint64(msgID)),
			slog.String("blob_hash", msg.Blob.Hash),
			slog.Any("err", err))
		return
	}
	if _, err := i.ingestor.IngestMessage(ctx, body); err != nil {
		i.logger.WarnContext(ctx, "maildmarc: ingest message",
			slog.String("activity", "internal"),
			slog.String("subsystem", "maildmarc"),
			slog.String("worker", "ingest"),
			slog.Uint64("message_id", uint64(msgID)),
			slog.Any("err", err))
	}
}

// recipientMatches reports whether the message's envelope To header
// carries an addr-spec whose lowercased local-part-with-@ has the
// configured prefix.
func (i *Intake) recipientMatches(msg store.Message) bool {
	if i.opts.RecipientPattern == "" {
		return true
	}
	candidates := []string{msg.Envelope.To, msg.Envelope.Cc, msg.Envelope.Bcc}
	for _, raw := range candidates {
		if raw == "" {
			continue
		}
		addrs, err := mail.ParseAddressList(raw)
		if err != nil {
			// Fall back to a substring scan; some reporters emit Subject
			// / Bcc with non-conformant address lists.
			if strings.Contains(strings.ToLower(raw), i.opts.RecipientPattern) {
				return true
			}
			continue
		}
		for _, a := range addrs {
			if strings.HasPrefix(strings.ToLower(a.Address), i.opts.RecipientPattern) {
				return true
			}
		}
	}
	return false
}

// readBody slurps the message blob into memory. DMARC reports are small
// (a few hundred KiB at most) so full-buffer is the right call here.
func (i *Intake) readBody(ctx context.Context, hash string) ([]byte, error) {
	if hash == "" {
		return nil, errors.New("empty blob hash")
	}
	r, err := i.blobs.Get(ctx, hash)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, reportSizeCap*2))
}
