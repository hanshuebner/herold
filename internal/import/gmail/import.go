package gmail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hanshuebner/herold/internal/store"
)

// Importer is the entry point for importing one Gmail Takeout archive
// into one principal. Construct it with the open store, the target
// principal, and Options; call Run to execute. Importer is single-shot
// — discard after Run.
type Importer struct {
	Store     store.Store
	Principal store.Principal
	Logger    *slog.Logger
	Options   Options
}

// Options controls importer behaviour. The zero value is usable for a
// dry run against an extracted Takeout directory.
type Options struct {
	// Source is the path to an extracted Takeout directory. The
	// importer walks it for .mbox + settings JSON files. Required.
	Source string
	// Locale, if non-empty, overrides locale auto-detection.
	Locale Locale
	// DryRun reports what the importer would do without writing to
	// the store. Used by REQ-IMPORT-61's --dry-run flag.
	DryRun bool
	// SkipMail, when true, only imports settings.
	SkipMail bool
	// SkipSettings, when true, only imports mail.
	SkipSettings bool
	// FreshPrincipal, when true, asserts the target principal has no
	// pre-existing messages and skips the initial Message-ID scan.
	// On a multi-million-message principal that scan can take seconds;
	// the operator opts in by promising the slate is clean. If the
	// promise is wrong, duplicates land — the importer makes no other
	// attempt to detect them.
	FreshPrincipal bool
	// LocaleSampleSize caps how many messages we read for locale
	// detection. Zero applies the default of 200.
	LocaleSampleSize int
	// ProgressEvery emits a progress log line every N imported
	// messages. Zero applies the default of 1000.
	ProgressEvery int
	// BatchSize is the number of messages buffered into one
	// InsertMessages call. The big-fsync win comes from batching, but
	// huge batches grow the in-memory buffer and lengthen rollback
	// surface on a per-batch failure. Zero applies the default of 500.
	BatchSize int
	// InternalizeImports controls whether imported messages with
	// external HTML image references are flagged for on-demand
	// rewrite at first JMAP Email/get
	// (17-external-images.md REQ-EXTIMG-91/-92):
	//   - "" or "on_demand" (default): flag eligible messages
	//   - "off": never flag; reads behave like passthrough mode
	//
	// Bulk-fetching at import time is deliberately not offered; the
	// requirement document explains why.
	InternalizeImports string
}

// Result is the per-job summary returned by Run. Counts are populated
// even on partial failure so the operator sees what happened before
// the abort.
type Result struct {
	// Locale is the detected (or forced) locale.
	Locale Locale
	// MessagesScanned is every mbox message the parser observed.
	MessagesScanned int
	// MessagesImported is the count of messages successfully written
	// to the store.
	MessagesImported int
	// MessagesSkippedDuplicate is the count skipped because their
	// Message-ID already existed in the principal's mail.
	MessagesSkippedDuplicate int
	// MessagesSkippedError is the count skipped because the per-message
	// parse or insert failed. The job continues.
	MessagesSkippedError int
	// MailboxesCreated is the number of new mailboxes (system roles +
	// user labels) the importer created.
	MailboxesCreated int
	// FiltersTranslated is the number of Gmail filter rules translated
	// into the imported-from-gmail Sieve script.
	FiltersTranslated int
	// BlockedAddresses is the count of addresses parsed from the
	// blocked-addresses settings file.
	BlockedAddresses int
	// ForwardingAddresses is the count from the forwarding settings file.
	ForwardingAddresses int
	// DelegatedIdentitiesCreated is the count of new send-as identities
	// created.
	DelegatedIdentitiesCreated int
	// ThreadsAssigned is the number of messages whose thread_id was
	// set by the post-import rethread sweep.
	ThreadsAssigned int
	// FirstErrors is a small (<= 32) sample of per-message errors with
	// byte offset and reason, surfaced to the operator at the end.
	FirstErrors []ImportError
}

// ImportError is one per-message failure with enough context to debug.
type ImportError struct {
	ByteOffset int64
	MessageID  string
	Reason     string
}

// Run executes the import. The caller is responsible for opening and
// closing imp.Store; Run does not.
func (imp *Importer) Run(ctx context.Context) (Result, error) {
	if imp.Logger == nil {
		imp.Logger = slog.Default()
	}
	log := imp.Logger.With("subsystem", "import-gmail", "principal", imp.Principal.CanonicalEmail)
	res := Result{}

	if imp.Options.Source == "" {
		return res, errors.New("import: Options.Source is required")
	}
	st, err := os.Stat(imp.Options.Source)
	if err != nil {
		return res, fmt.Errorf("import: stat source: %w", err)
	}
	if !st.IsDir() {
		return res, errors.New("import: Options.Source must be a directory; extract the Takeout archive first (tar xzf takeout.tgz -C /tmp/takeout)")
	}

	mboxPath, settingsFiles, err := scanSourceDirectory(imp.Options.Source)
	if err != nil {
		return res, fmt.Errorf("import: scan source: %w", err)
	}
	log.Info("source scanned",
		"activity", "user",
		"mbox_present", mboxPath != "",
		"settings_files", len(settingsFiles))

	// Locale detection: sniff the first N messages for X-Gmail-Labels
	// samples unless the operator forced a locale.
	locale := imp.Options.Locale
	if locale == "" && mboxPath != "" {
		locale, err = detectLocaleFromMbox(mboxPath, imp.sampleSize())
		if err != nil {
			return res, fmt.Errorf("import: detect locale: %w", err)
		}
	}
	if locale == "" {
		locale = "en"
	}
	res.Locale = locale
	log.Info("locale picked", "activity", "user", "locale", string(locale))

	// Resolve / create system mailboxes once up front.
	systemMailboxes, err := imp.resolveSystemMailboxes(ctx)
	if err != nil {
		return res, fmt.Errorf("import: system mailboxes: %w", err)
	}

	userLabelCache := newMailboxCache()

	// Seed the in-memory dedup set with the principal's existing blob
	// hashes. Content-hash dedup is what we actually want: a small but
	// real fraction of inbound mail (notably spam) reuses Message-ID
	// across distinct messages, and dedup-by-Message-ID would silently
	// lose those distinct-but-same-ID messages. The blob hash is the
	// canonical-CRLF BLAKE3 digest the blob store already computes
	// (REQ-STORE-10), so two genuinely identical messages collapse to
	// one row and two distinct messages with a colliding Message-ID
	// are kept apart.
	dedup := newDedupSet()
	if !imp.Options.FreshPrincipal {
		hashes, derr := imp.Store.Meta().ListPrincipalBlobHashes(ctx, imp.Principal.ID)
		if derr != nil {
			return res, fmt.Errorf("import: seed dedup set: %w", derr)
		}
		for _, h := range hashes {
			dedup.add(h)
		}
		log.Info("dedup set seeded", "activity", "user", "existing_blob_hashes", dedup.size())
	}

	if !imp.Options.SkipMail && mboxPath != "" {
		if err := imp.importMail(ctx, log, mboxPath, locale, systemMailboxes, userLabelCache, dedup, &res); err != nil {
			return res, err
		}
	}

	// Post-import rethread sweep. The bulk InsertMessages path skips
	// per-message thread resolution to avoid the O(log n) lookup that
	// grows with the messages-table size; we run a single amortised
	// pass here that builds the (env_message_id -> id) map once and
	// updates every threadless row in one transaction.
	if !imp.Options.SkipMail && !imp.Options.DryRun && res.MessagesImported > 0 {
		log.Info("rethread: starting", "activity", "user")
		rethreadStart := time.Now()
		n, rerr := imp.Store.Meta().RethreadPrincipal(ctx, imp.Principal.ID)
		if rerr != nil {
			return res, fmt.Errorf("import: rethread: %w", rerr)
		}
		res.ThreadsAssigned = n
		log.Info("rethread: complete",
			"activity", "user",
			"messages_threaded", n,
			"duration_ms", time.Since(rethreadStart).Milliseconds())
	}

	if !imp.Options.SkipSettings {
		if err := imp.importSettings(ctx, log, settingsFiles, locale, &res); err != nil {
			return res, err
		}
	}

	res.MailboxesCreated = userLabelCache.created + systemMailboxes.created
	log.Info("import complete",
		"activity", "user",
		"messages_imported", res.MessagesImported,
		"messages_skipped_dup", res.MessagesSkippedDuplicate,
		"messages_skipped_error", res.MessagesSkippedError,
		"mailboxes_created", res.MailboxesCreated,
		"filters", res.FiltersTranslated)
	return res, nil
}

func (imp *Importer) sampleSize() int {
	if imp.Options.LocaleSampleSize > 0 {
		return imp.Options.LocaleSampleSize
	}
	return 200
}

func (imp *Importer) progressEvery() int {
	if imp.Options.ProgressEvery > 0 {
		return imp.Options.ProgressEvery
	}
	return 1000
}

func (imp *Importer) batchSize() int {
	if imp.Options.BatchSize > 0 {
		return imp.Options.BatchSize
	}
	return 500
}

// scanSourceDirectory walks dir to find the first .mbox file and every
// .json settings file. Returns mboxPath ("" if none) plus the slice of
// settings paths in lexical order.
func scanSourceDirectory(dir string) (string, []string, error) {
	var mboxPath string
	var settings []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".mbox":
			if mboxPath == "" {
				mboxPath = path
			}
		case ".json":
			settings = append(settings, path)
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	sort.Strings(settings)
	return mboxPath, settings, nil
}

// detectLocaleFromMbox reads up to sampleSize messages from path,
// extracts X-Gmail-Labels values, and returns the detected locale.
func detectLocaleFromMbox(path string, sampleSize int) (Locale, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	r := NewMboxReader(f)
	samples := make([][]string, 0, sampleSize)
	for i := 0; i < sampleSize; i++ {
		msg, rerr := r.Next()
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return "", rerr
		}
		labels := extractGmailLabelsHeader(msg.Body)
		if len(labels) > 0 {
			samples = append(samples, labels)
		}
	}
	return DetectLocale(samples), nil
}

// extractGmailLabelsHeader reads the X-Gmail-Labels header from a raw
// RFC 5322 message and returns its parsed token list. Empty when the
// header is absent.
func extractGmailLabelsHeader(body []byte) []string {
	// Header block ends at the first blank line.
	end := bytes.Index(body, []byte("\n\n"))
	if end < 0 {
		end = len(body)
	}
	headerBlock := body[:end]
	// Walk lines, joining continuation lines (starting with whitespace).
	lines := bytes.Split(headerBlock, []byte("\n"))
	var values []string
	cur := ""
	emit := func() {
		const prefix = "x-gmail-labels:"
		lc := strings.ToLower(cur)
		if strings.HasPrefix(lc, prefix) {
			values = append(values, strings.TrimSpace(cur[len(prefix):]))
		}
		cur = ""
	}
	for _, l := range lines {
		s := strings.TrimRight(string(l), "\r")
		if s == "" {
			break
		}
		if len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
			cur += " " + strings.TrimSpace(s)
			continue
		}
		if cur != "" {
			emit()
		}
		cur = s
	}
	if cur != "" {
		emit()
	}
	if len(values) == 0 {
		return nil
	}
	return ParseGmailLabels(values[0])
}

// systemMailboxRefs holds the resolved mailbox IDs for each Gmail role
// the importer needs. Missing roles are created on first use.
type systemMailboxRefs struct {
	imp     *Importer
	ids     map[SystemLabel]store.MailboxID
	created int
}

// roleNames is the canonical herold mailbox-name-by-role lookup. The
// names mirror what bootstrap creates for fresh principals.
var roleNames = map[SystemLabel]string{
	SystemLabelInbox:  "INBOX",
	SystemLabelSent:   "Sent",
	SystemLabelDrafts: "Drafts",
	SystemLabelSpam:   "Junk",
	SystemLabelTrash:  "Trash",
}

// roleAttributes is the SPECIAL-USE bit for each role.
var roleAttributes = map[SystemLabel]store.MailboxAttributes{
	SystemLabelInbox:  store.MailboxAttrInbox,
	SystemLabelSent:   store.MailboxAttrSent,
	SystemLabelDrafts: store.MailboxAttrDrafts,
	SystemLabelSpam:   store.MailboxAttrJunk,
	SystemLabelTrash:  store.MailboxAttrTrash,
}

func (imp *Importer) resolveSystemMailboxes(ctx context.Context) (*systemMailboxRefs, error) {
	refs := &systemMailboxRefs{imp: imp, ids: map[SystemLabel]store.MailboxID{}}
	// Pre-populate ids map from existing mailboxes — avoid creating
	// duplicates when bootstrap (or earlier imports) already left a
	// role mailbox in place.
	mboxes, err := imp.Store.Meta().ListMailboxes(ctx, imp.Principal.ID)
	if err != nil {
		return nil, err
	}
	for _, mb := range mboxes {
		for sym, attr := range roleAttributes {
			if mb.Attributes&attr != 0 {
				refs.ids[sym] = mb.ID
			}
		}
	}
	return refs, nil
}

// idFor returns the MailboxID for sym, creating the mailbox if absent.
// Honours dry-run by returning a sentinel zero id and not calling the
// store.
func (r *systemMailboxRefs) idFor(ctx context.Context, sym SystemLabel) (store.MailboxID, error) {
	if id, ok := r.ids[sym]; ok {
		return id, nil
	}
	name, ok := roleNames[sym]
	if !ok {
		return 0, fmt.Errorf("import: no canonical name for role %v", sym)
	}
	if r.imp.Options.DryRun {
		// Use a synthetic id for dry-run accounting; the import path
		// will refuse to call InsertMessage anyway.
		r.created++
		r.ids[sym] = 0
		return 0, nil
	}
	mb := store.Mailbox{
		PrincipalID: r.imp.Principal.ID,
		Name:        name,
		Attributes:  roleAttributes[sym],
	}
	var created store.Mailbox
	err := retryOnBusy(ctx, "insert system mailbox", func() error {
		var ierr error
		created, ierr = r.imp.Store.Meta().InsertMailbox(ctx, mb)
		return ierr
	})
	if err != nil {
		// If the conflict is a name clash, retry by reading the
		// existing mailbox by name; otherwise propagate.
		if errors.Is(err, store.ErrConflict) {
			var existing store.Mailbox
			gerr := retryOnBusy(ctx, "resolve system mailbox", func() error {
				var lerr error
				existing, lerr = r.imp.Store.Meta().GetMailboxByName(ctx, r.imp.Principal.ID, name)
				return lerr
			})
			if gerr != nil {
				return 0, fmt.Errorf("import: resolve %s after conflict: %w", name, gerr)
			}
			r.ids[sym] = existing.ID
			return existing.ID, nil
		}
		return 0, fmt.Errorf("import: create %s: %w", name, err)
	}
	r.ids[sym] = created.ID
	r.created++
	return created.ID, nil
}

// mailboxCache memoises label-name -> MailboxID lookups so a 50k-message
// mbox doesn't InsertMailbox 50k times for the same user label.
type mailboxCache struct {
	byName  map[string]store.MailboxID
	created int
}

func newMailboxCache() *mailboxCache {
	return &mailboxCache{byName: map[string]store.MailboxID{}}
}

func (c *mailboxCache) idForLabel(ctx context.Context, imp *Importer, name string) (store.MailboxID, error) {
	if id, ok := c.byName[name]; ok {
		return id, nil
	}
	if imp.Options.DryRun {
		c.created++
		c.byName[name] = 0
		return 0, nil
	}
	var existing store.Mailbox
	err := retryOnBusy(ctx, "lookup user label", func() error {
		var lerr error
		existing, lerr = imp.Store.Meta().GetMailboxByName(ctx, imp.Principal.ID, name)
		return lerr
	})
	if err == nil {
		c.byName[name] = existing.ID
		return existing.ID, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return 0, fmt.Errorf("import: lookup label %q: %w", name, err)
	}
	mb := store.Mailbox{PrincipalID: imp.Principal.ID, Name: name}
	var created store.Mailbox
	cerr := retryOnBusy(ctx, "create user label", func() error {
		var ierr error
		created, ierr = imp.Store.Meta().InsertMailbox(ctx, mb)
		return ierr
	})
	if cerr != nil {
		return 0, fmt.Errorf("import: create label %q: %w", name, cerr)
	}
	c.byName[name] = created.ID
	c.created++
	return created.ID, nil
}

// dedupSet is the in-memory Message-ID set used by the import hot
// path to skip per-message GetMessageByMessageIDHeader calls. It is
// not threadsafe; the importer is single-goroutine.
type dedupSet struct {
	m map[string]struct{}
}

func newDedupSet() *dedupSet      { return &dedupSet{m: make(map[string]struct{}, 1024)} }
func (d *dedupSet) add(id string) { d.m[id] = struct{}{} }
func (d *dedupSet) has(id string) bool {
	_, ok := d.m[id]
	return ok
}
func (d *dedupSet) size() int { return len(d.m) }

// importMail streams the mbox and inserts each message.
func (imp *Importer) importMail(
	ctx context.Context,
	log *slog.Logger,
	mboxPath string,
	locale Locale,
	systemRefs *systemMailboxRefs,
	userCache *mailboxCache,
	dedup *dedupSet,
	res *Result,
) error {
	f, err := os.Open(mboxPath)
	if err != nil {
		return fmt.Errorf("import: open mbox: %w", err)
	}
	defer f.Close()
	mboxReader := NewMboxReader(f)

	progressEvery := imp.progressEvery()
	batchSize := imp.batchSize()

	// Per-chunk rate accounting (see chunkMsgsPerSec).
	startedAt := time.Now()
	lastTickAt := startedAt
	lastTickScanned := 0

	// Pending batch for InsertMessages. preparedMessage carries the
	// store-shaped item alongside the dedup key so a successful flush
	// can update the in-memory dedup set.
	pending := make([]preparedMessage, 0, batchSize)

	flushBatch := func() error {
		if len(pending) == 0 {
			return nil
		}
		// In-batch dedup: prepareOneMessage checks the persistent
		// dedup set, but two messages with the same content within
		// a single batch will both pass that check (the persistent
		// set only updates on flush). Drop the second-and-subsequent
		// occurrences here so InsertMessages sees one row per
		// canonical hash. The drop counts toward MessagesSkippedDuplicate
		// just like cross-batch dupes.
		seen := make(map[string]struct{}, len(pending))
		filtered := pending[:0]
		for _, p := range pending {
			if p.dedupKey != "" {
				if _, dup := seen[p.dedupKey]; dup {
					res.MessagesSkippedDuplicate++
					continue
				}
				seen[p.dedupKey] = struct{}{}
			}
			filtered = append(filtered, p)
		}
		pending = filtered
		if len(pending) == 0 {
			return nil
		}
		items := make([]store.InsertMessageItem, len(pending))
		for i, p := range pending {
			items[i] = store.InsertMessageItem{Message: p.msg, Targets: p.targets}
		}
		err := retryOnBusy(ctx, "insert messages batch", func() error {
			_, err := imp.Store.Meta().InsertMessages(ctx, items, store.InsertMessagesOptions{SkipThreading: true})
			return err
		})
		if err != nil {
			// Fall back to per-message inserts so a single bad item
			// doesn't sink the whole batch. Each failure logs and is
			// counted; the dedup set is updated only on success.
			log.Warn("insert messages batch failed; falling back to per-message",
				"activity", "user",
				"batch_size", len(pending),
				"err", err.Error())
			for _, p := range pending {
				ferr := retryOnBusy(ctx, "insert message", func() error {
					_, _, e := imp.Store.Meta().InsertMessage(ctx, p.msg, p.targets)
					return e
				})
				if ferr != nil {
					res.MessagesSkippedError++
					if len(res.FirstErrors) < 32 {
						res.FirstErrors = append(res.FirstErrors, ImportError{
							ByteOffset: p.byteOffset,
							Reason:     ferr.Error(),
						})
					}
					log.Warn("message import failed",
						"activity", "user",
						"offset", p.byteOffset,
						"err", ferr.Error())
					continue
				}
				if p.dedupKey != "" {
					dedup.add(p.dedupKey)
				}
				res.MessagesImported++
			}
			pending = pending[:0]
			return nil
		}
		// Batch committed wholesale.
		for _, p := range pending {
			if p.dedupKey != "" {
				dedup.add(p.dedupKey)
			}
			res.MessagesImported++
		}
		pending = pending[:0]
		return nil
	}

	emitProgress := func() {
		now := time.Now()
		chunkRate := chunkMsgsPerSec(res.MessagesScanned-lastTickScanned, now.Sub(lastTickAt))
		overallRate := chunkMsgsPerSec(res.MessagesScanned, now.Sub(startedAt))
		log.Info("import progress",
			"activity", "user",
			"scanned", res.MessagesScanned,
			"imported", res.MessagesImported,
			"skipped_dup", res.MessagesSkippedDuplicate,
			"chunk_msgs_per_sec", chunkRate,
			"overall_msgs_per_sec", overallRate)
		lastTickAt = now
		lastTickScanned = res.MessagesScanned
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, rerr := mboxReader.Next()
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			if ferr := flushBatch(); ferr != nil {
				return ferr
			}
			return fmt.Errorf("import: mbox stream: %w", rerr)
		}
		res.MessagesScanned++

		prep, dec, err := imp.prepareOneMessage(ctx, raw, locale, systemRefs, userCache, dedup, res)
		if err != nil {
			res.MessagesSkippedError++
			if len(res.FirstErrors) < 32 {
				res.FirstErrors = append(res.FirstErrors, ImportError{
					ByteOffset: raw.ByteOffset,
					Reason:     err.Error(),
				})
			}
			log.Warn("message import failed",
				"activity", "user",
				"offset", raw.ByteOffset,
				"err", err.Error())
			goto progress
		}
		if dec == prepDecisionSkip {
			goto progress
		}
		if dec == prepDecisionDryRun {
			res.MessagesImported++
			goto progress
		}
		pending = append(pending, prep)
		if len(pending) >= batchSize {
			if err := flushBatch(); err != nil {
				return err
			}
		}
	progress:
		if res.MessagesScanned%progressEvery == 0 {
			emitProgress()
		}
	}
	if err := flushBatch(); err != nil {
		return err
	}
	return nil
}

// preparedMessage is one batch-buffered insert. byteOffset is for
// error reporting; dedupKey is the Message-ID we added to the dedup
// set on successful commit.
type preparedMessage struct {
	msg        store.Message
	targets    []store.MessageMailbox
	byteOffset int64
	dedupKey   string
}

type prepDecision int

const (
	prepDecisionInsert prepDecision = iota
	prepDecisionSkip
	prepDecisionDryRun
)

// retryOnBusy wraps a store call that may fail with a SQLite contention
// error and retries it with exponential backoff. The errors we retry:
//
//   - SQLITE_BUSY (5) — generic "database is locked"; busy_timeout=30s
//     usually handles this driver-side, but heavy contention can still
//     surface it to us.
//   - SQLITE_BUSY_SNAPSHOT (517) — WAL-specific: the connection's
//     snapshot moved out from under it. busy_timeout does NOT retry
//     this; only the application can.
//
// The importer is the secondary writer when the herold server is
// running concurrently against the same SQLite file; treating the
// bulk-insert path as retry-able lets the import coexist with live
// FTS / queue / SMTP traffic instead of bouncing off it. Backoff
// caps at 5s per attempt; the total budget is bounded by maxRetryBudget
// so a permanent lock (a stuck server) eventually surfaces.
//
// On retry-after-success the helper is silent — flooding stderr with
// "recovered" lines per message would defeat the progress signal.
func retryOnBusy(ctx context.Context, op string, f func() error) error {
	const (
		baseBackoff      = 100 * time.Millisecond
		maxBackoff       = 5 * time.Second
		maxRetryBudget   = 60 * time.Second
		maxRetryAttempts = 30
	)
	deadline := time.Now().Add(maxRetryBudget)
	backoff := baseBackoff
	var lastErr error
	for attempt := 0; attempt < maxRetryAttempts; attempt++ {
		err := f()
		if err == nil {
			return nil
		}
		if !isBusyError(err) {
			return err
		}
		lastErr = err
		if time.Now().After(deadline) {
			break
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
		_ = op // op is reserved for a future debug-level "retrying" log call
	}
	return lastErr
}

// isBusyError reports whether err is a SQLite busy / busy-snapshot
// error. We pattern-match on the error string rather than depending
// on modernc.org/sqlite's error type — the package boundary is in
// internal/storesqlite, and the gmail importer should not pull a
// driver dependency just to classify retries.
func isBusyError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "database is locked")
}

// chunkMsgsPerSec returns count / elapsed.Seconds() rounded to one
// decimal, or 0 when elapsed is zero or negative (an edge case that
// can show up if the system clock jumps backward between progress
// ticks; report zero rather than panic on division-by-zero).
func chunkMsgsPerSec(count int, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	rate := float64(count) / elapsed.Seconds()
	// Round to one decimal place so progress lines stay readable.
	return float64(int(rate*10+0.5)) / 10
}

// prepareOneMessage parses the envelope, runs dedup, builds the
// (Message, []MessageMailbox) pair, and stores the body blob. It does
// NOT call InsertMessage — the caller batches inserts and decides when
// to flush. Returns:
//
//   - (prep, prepDecisionInsert, nil) when the message should be inserted.
//   - (zero, prepDecisionSkip, nil) when the message is a duplicate.
//   - (zero, prepDecisionDryRun, nil) when DryRun is set.
//   - (zero, _, err) on parse / mailbox / blob errors.
func (imp *Importer) prepareOneMessage(
	ctx context.Context,
	raw MboxMessage,
	locale Locale,
	systemRefs *systemMailboxRefs,
	userCache *mailboxCache,
	dedup *dedupSet,
	res *Result,
) (preparedMessage, prepDecision, error) {
	envelope, err := parseEnvelopeOnly(raw.Body)
	if err != nil {
		return preparedMessage{}, prepDecisionSkip, fmt.Errorf("envelope: %w", err)
	}

	// Decide where the message goes.
	labelTokens := extractGmailLabelsHeader(raw.Body)
	decision := Map(labelTokens, locale)

	// Build target list. We materialise one MessageMailbox per role +
	// per user label. If neither produces a target, the message is
	// archived — i.e. lives in the store but is not in any mailbox.
	// Herold's model requires at least one target on InsertMessage, so
	// we file archived mail into INBOX as the conservative default.
	// (Operators preferring "All Mail-style archive" reorganise post-
	// import via the suite.)
	targets := make([]store.MessageMailbox, 0, len(decision.Roles)+len(decision.UserLabels))
	flags := store.MessageFlags(0)
	if decision.Seen {
		flags |= store.MessageFlagSeen
	}
	for _, kw := range decision.Keywords {
		switch kw {
		case `\Draft`:
			flags |= store.MessageFlagDraft
		case `\Flagged`:
			flags |= store.MessageFlagFlagged
		}
	}
	keywords := normaliseKeywords(decision.Keywords)

	for _, role := range decision.Roles {
		id, err := systemRefs.idFor(ctx, role)
		if err != nil {
			return preparedMessage{}, prepDecisionSkip, err
		}
		targets = append(targets, store.MessageMailbox{
			MailboxID: id,
			Flags:     flags,
			Keywords:  keywords,
		})
	}
	for _, lbl := range decision.UserLabels {
		id, err := userCache.idForLabel(ctx, imp, lbl)
		if err != nil {
			return preparedMessage{}, prepDecisionSkip, err
		}
		targets = append(targets, store.MessageMailbox{
			MailboxID: id,
			Flags:     flags,
			Keywords:  keywords,
		})
	}
	if len(targets) == 0 {
		// Archived (no Inbox + no user labels). Park in INBOX so the
		// message remains accessible via IMAP / JMAP. Without this
		// fallback the InsertMessage signature requires at least one
		// target; storing the message archived-and-unfileable would
		// drop it into a void.
		id, err := systemRefs.idFor(ctx, SystemLabelInbox)
		if err != nil {
			return preparedMessage{}, prepDecisionSkip, err
		}
		targets = append(targets, store.MessageMailbox{
			MailboxID: id,
			Flags:     flags,
			Keywords:  keywords,
		})
	}

	if imp.Options.DryRun {
		return preparedMessage{}, prepDecisionDryRun, nil
	}

	// Store the blob (idempotent on identical canonical content) so we
	// can use the resulting BLAKE3 hash as the dedup key. Two messages
	// whose bodies canonicalise to the same bytes get the same hash;
	// two messages with the same Message-ID but different bytes get
	// different hashes — that's exactly the right semantic for spam
	// senders that recycle Message-IDs.
	ref, err := imp.Store.Blobs().Put(ctx, bytes.NewReader(raw.Body))
	if err != nil {
		return preparedMessage{}, prepDecisionSkip, fmt.Errorf("blob put: %w", err)
	}

	if dedup.has(ref.Hash) {
		res.MessagesSkippedDuplicate++
		return preparedMessage{}, prepDecisionSkip, nil
	}

	internalDate := envelopeInternalDate(raw.FromLine, envelope.Date)
	msg := store.Message{
		PrincipalID:  imp.Principal.ID,
		InternalDate: internalDate,
		ReceivedAt:   internalDate,
		Size:         ref.Size,
		Blob:         ref,
		Envelope:     envelope,
	}
	// REQ-EXTIMG-91 / REQ-EXTIMG-93: flag for on-demand rewrite at
	// first read when the body looks like HTML with at least one
	// external http(s) reference. Cheap substring scan; false
	// positives just trigger a no-op rewrite at first read; false
	// negatives leave the message unrewritten (no privacy harm).
	if imp.shouldFlagOnDemand() && hasExternalHTMLImage(raw.Body) {
		msg.InternalizePending = true
	}
	return preparedMessage{
		msg:        msg,
		targets:    targets,
		byteOffset: raw.ByteOffset,
		dedupKey:   ref.Hash,
	}, prepDecisionInsert, nil
}

// shouldFlagOnDemand reports whether the importer should set
// InternalizePending on eligible messages. Operator policy is set
// through Options.InternalizeImports; default (empty / "on_demand")
// flags, "off" suppresses.
func (imp *Importer) shouldFlagOnDemand() bool {
	switch imp.Options.InternalizeImports {
	case "", "on_demand":
		return true
	case "off":
		return false
	}
	return true
}

// hasExternalHTMLImage is the cheap heuristic that gates the pending
// flag: substring scan for an <img tag and an http URL anywhere in
// the body. The on-demand pass parses the body precisely and may
// find no actual external refs (CSS-only images, false positives in
// quoted text, etc.), in which case it clears the flag and the cost
// is one parse — orders of magnitude cheaper than the alternative
// of parsing every imported message at import time.
func hasExternalHTMLImage(body []byte) bool {
	return bytes.Contains(body, []byte("<img")) &&
		(bytes.Contains(body, []byte("http://")) || bytes.Contains(body, []byte("https://")))
}

// parseEnvelopeOnly extracts the store.Envelope summary by reading
// only the RFC 5322 header block. We deliberately do NOT walk the MIME
// body tree: real-world Gmail Takeout mboxes contain a non-trivial
// fraction of messages with truncated multipart boundaries, malformed
// base64 inside leaf parts, or charset declarations that don't match
// the bytes — every one of those would fail an enmime-strict parse,
// but every one stores fine as a raw blob. The downstream JMAP / IMAP
// render path re-parses on serve and will surface those quirks one
// message at a time, where they belong.
//
// Three-tier strategy:
//
//  1. net/mail.ReadMessage — the standard library RFC 5322 reader.
//     Handles continuation lines, RFC 2047 encoded words via the
//     mime.WordDecoder, and the bulk of well-formed mail.
//  2. Manual line-by-line header parse — used when stdlib rejects the
//     header block (e.g. invalid bytes in a header value, missing
//     blank line). Last-resort: extract whatever Message-ID + Subject
//     we can find and continue.
//  3. Empty envelope — if even the manual parse finds nothing, return
//     a zero envelope. The blob still stores; the message is
//     retrievable by content hash; downstream views show "(no
//     subject)" / "(unknown sender)".
func parseEnvelopeOnly(body []byte) (store.Envelope, error) {
	if env, ok := envelopeFromStdlib(body); ok {
		return env, nil
	}
	return envelopeFromManualScan(body), nil
}

// envelopeFromStdlib uses net/mail.ReadMessage to parse the header
// block. Returns (envelope, true) on success; (zero, false) when the
// stdlib reader rejects the message.
func envelopeFromStdlib(body []byte) (store.Envelope, bool) {
	msg, err := mail.ReadMessage(bytes.NewReader(body))
	if err != nil {
		return store.Envelope{}, false
	}
	h := msg.Header
	dec := mime.WordDecoder{}
	decode := func(s string) string {
		if s == "" {
			return ""
		}
		out, err := dec.DecodeHeader(s)
		if err != nil {
			return s
		}
		return out
	}
	return store.Envelope{
		Subject:    decode(h.Get("Subject")),
		From:       decode(h.Get("From")),
		To:         decode(h.Get("To")),
		Cc:         decode(h.Get("Cc")),
		Bcc:        decode(h.Get("Bcc")),
		ReplyTo:    decode(h.Get("Reply-To")),
		MessageID:  strings.Trim(h.Get("Message-ID"), "<> \t"),
		InReplyTo:  strings.Trim(h.Get("In-Reply-To"), "<> \t"),
		References: h.Get("References"),
		Date:       parseDateBestEffort(h.Get("Date")),
	}, true
}

// envelopeFromManualScan walks header lines by hand. Continuation
// lines (leading whitespace) are joined to the previous header. Lines
// with bytes net/mail rejects (control chars, etc.) are tolerated.
// Returns whatever fields could be extracted; never errors.
func envelopeFromManualScan(body []byte) store.Envelope {
	end := bytes.Index(body, []byte("\n\n"))
	if end < 0 {
		end = len(body)
	}
	headerBlock := body[:end]
	lines := bytes.Split(headerBlock, []byte("\n"))

	headers := map[string]string{}
	cur := ""
	curName := ""
	emit := func() {
		if curName == "" {
			return
		}
		// Last value wins for duplicates — same as net/mail.Header.Get.
		headers[strings.ToLower(curName)] = strings.TrimSpace(cur)
	}
	for _, l := range lines {
		s := strings.TrimRight(string(l), "\r")
		if s == "" {
			break
		}
		if s[0] == ' ' || s[0] == '\t' {
			cur += " " + strings.TrimSpace(s)
			continue
		}
		emit()
		colon := strings.Index(s, ":")
		if colon < 0 {
			cur = ""
			curName = ""
			continue
		}
		curName = s[:colon]
		cur = strings.TrimSpace(s[colon+1:])
	}
	emit()

	dec := mime.WordDecoder{}
	decode := func(s string) string {
		if s == "" {
			return ""
		}
		out, err := dec.DecodeHeader(s)
		if err != nil {
			return s
		}
		return out
	}
	return store.Envelope{
		Subject:    decode(headers["subject"]),
		From:       decode(headers["from"]),
		To:         decode(headers["to"]),
		Cc:         decode(headers["cc"]),
		Bcc:        decode(headers["bcc"]),
		ReplyTo:    decode(headers["reply-to"]),
		MessageID:  strings.Trim(headers["message-id"], "<> \t"),
		InReplyTo:  strings.Trim(headers["in-reply-to"], "<> \t"),
		References: headers["references"],
		Date:       parseDateBestEffort(headers["date"]),
	}
}

// parseDateBestEffort parses the Date header. Empty / unparseable
// inputs return time.Time{} (the zero value); the importer falls back
// to the From-line tail in that case.
func parseDateBestEffort(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := mail.ParseDate(s); err == nil {
		return t
	}
	return time.Time{}
}

// envelopeInternalDate prefers the Date: header; falls back to the
// envelope-from line ("From sender ctime-string"). The mbox From line
// for Gmail-emitted mboxes carries a usable ctime-like timestamp at the
// tail; on parse failure we use time.Now() as a last resort.
func envelopeInternalDate(fromLine string, date time.Time) time.Time {
	if !date.IsZero() {
		return date
	}
	// Try to parse the From-line tail. Gmail emits "From <id> Wed May
	// 06 18:21:37 +0000 2026" — RFC 2822-ish but with the year at the
	// end. We try a couple of layouts.
	tail := strings.TrimSpace(fromLine)
	if i := strings.Index(tail, " "); i >= 0 {
		tail = strings.TrimSpace(tail[i+1:])
		if i := strings.Index(tail, " "); i >= 0 {
			tail = strings.TrimSpace(tail[i+1:])
		}
	}
	for _, layout := range []string{
		"Mon Jan 02 15:04:05 -0700 2006",
		"Mon Jan _2 15:04:05 -0700 2006",
		"Mon Jan 02 15:04:05 2006",
	} {
		if t, err := time.Parse(layout, tail); err == nil {
			return t
		}
	}
	return time.Now()
}

// normaliseKeywords lowercases keywords without backslash-prefix (RFC
// 5788 user keywords) and drops the system-flag-style ones (which we
// already mapped onto MessageFlags). Stable order: sort ASCII.
func normaliseKeywords(in []string) []string {
	out := make([]string, 0, len(in))
	for _, k := range in {
		if strings.HasPrefix(k, `\`) {
			// system flag — already on MessageFlags or unsupported.
			continue
		}
		out = append(out, strings.ToLower(k))
	}
	sort.Strings(out)
	return out
}

// importSettings reads each settings JSON, classifies it, and applies
// the corresponding herold-side action. Filters become a Sieve script;
// blocked / forwarding / delegated addresses are recorded in res for
// the operator to act on (REQ-IMPORT-44..46).
func (imp *Importer) importSettings(
	ctx context.Context,
	log *slog.Logger,
	files []string,
	locale Locale,
	res *Result,
) error {
	var allFilters []GmailFilter
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			log.Warn("settings: read failed", "activity", "user", "path", path, "err", err.Error())
			continue
		}
		kind, filtersFile, addrList, cerr := ClassifyJSON(raw)
		if cerr != nil {
			log.Warn("settings: classify failed", "activity", "user", "path", path, "err", cerr.Error())
			continue
		}
		switch kind {
		case SettingsKindFilters:
			if filtersFile != nil {
				allFilters = append(allFilters, filtersFile.Filter...)
			}
		case SettingsKindAddressList:
			if addrList == nil {
				continue
			}
			hint := AddressListHint(filepath.Base(path))
			switch hint {
			case "blocked":
				res.BlockedAddresses += len(addrList.Addresses)
			case "forwarding":
				res.ForwardingAddresses += len(addrList.Addresses)
			case "delegated":
				if !imp.Options.DryRun {
					for _, addr := range addrList.Addresses {
						if err := imp.createDelegatedIdentity(ctx, addr); err != nil {
							log.Warn("settings: identity create failed",
								"activity", "user", "address", addr, "err", err.Error())
							continue
						}
						res.DelegatedIdentitiesCreated++
					}
				} else {
					res.DelegatedIdentitiesCreated += len(addrList.Addresses)
				}
			default:
				log.Warn("settings: unrecognised address-list filename",
					"activity", "user", "path", path)
			}
		}
	}

	if len(allFilters) > 0 {
		res.FiltersTranslated = len(allFilters)
		if !imp.Options.DryRun {
			script := TranslateFiltersToSieve(allFilters, locale, nil)
			err := retryOnBusy(ctx, "put sieve script", func() error {
				return imp.Store.Meta().PutSieveScriptByName(ctx, imp.Principal.ID, "imported-from-gmail", script)
			})
			if err != nil {
				return fmt.Errorf("import: store sieve script: %w", err)
			}
		}
	}
	return nil
}

// createDelegatedIdentity inserts a JMAP Identity for an external
// "send as" address. The identity is disabled-by-default semantics
// (MayDelete=true; the user must wire SMTP credentials separately for
// it to be usable) per REQ-IMPORT-46.
func (imp *Importer) createDelegatedIdentity(ctx context.Context, addr string) error {
	if _, err := mail.ParseAddress(addr); err != nil {
		return fmt.Errorf("invalid address: %w", err)
	}
	id := JMAPIdentityIDFor(addr)
	row := store.JMAPIdentity{
		ID:          id,
		PrincipalID: imp.Principal.ID,
		Email:       addr,
		MayDelete:   true,
	}
	return retryOnBusy(ctx, "insert identity", func() error {
		return imp.Store.Meta().InsertJMAPIdentity(ctx, row)
	})
}

// JMAPIdentityIDFor synthesises a stable JMAP identity ID for an
// imported address. The wire form is decimal-only so we hash the
// address into a 64-bit unsigned then format as decimal. Two imports
// of the same address produce the same id, so re-running an import
// against an already-present identity hits the conflict path
// gracefully.
func JMAPIdentityIDFor(addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	var h uint64 = 14695981039346656037 // FNV-1a offset basis
	for i := 0; i < len(addr); i++ {
		h ^= uint64(addr[i])
		h *= 1099511628211
	}
	return fmt.Sprintf("%d", h)
}
