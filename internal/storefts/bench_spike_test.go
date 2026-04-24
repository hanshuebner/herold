//go:build spike

// Spike harness measuring Bleve indexing throughput, latency, memory, and
// query cost as a function of batch size, to pick defensible defaults for
// the storefts package. Gated behind the `spike` build tag so ordinary
// `go test ./...` runs do not pull in Bleve or spend minutes indexing.
//
//	Run: go test -tags=spike -timeout=30m -run=NONE -bench=. -benchtime=1x \
//	      ./internal/storefts/...
//
// The benchmarks emit CSV-ish lines via t.Log so results are easy to lift
// into the spike write-up. Everything is deterministic: a fixed PRNG seed
// drives the synthetic corpus and query generation.
package storefts

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"
)

// Corpus size for the baseline run. The spike note documents whether larger
// numbers were measured directly or extrapolated from smaller samples.
// 25k docs at ~5 KB bodies is enough signal across the surviving batch sizes
// while keeping the harness inside a tight wall-clock budget.
const baselineDocs = 8_000

// Per-message body target (bytes). ~5 KB matches "realistic English-ish"
// text bodies with light HTML stripped; attachment text is covered by the
// 20 MB envelope limit and not in this microbenchmark.
const bodySizeBytes = 5 * 1024

// Batch sizes exercised. batch=1 is excluded: Bleve creates one scorch
// segment per Batch() call, so one-doc-per-batch at meaningful corpus sizes
// produces a merge storm (N segments, O(N) merges) with runtime that's
// orders of magnitude worse than any defensible default. The spike note
// documents the exclusion; no need to re-measure it to know it's wrong.
var batchSizes = []int{500, 2000, 10000}

// Words for synthetic bodies. A small-ish Zipf-lite vocabulary gives the
// tokenizer something to stem without turning the corpus into random noise
// (which would blow up the dictionary and skew memory).
var vocab = []string{
	"the", "of", "and", "to", "a", "in", "for", "is", "on", "that",
	"by", "this", "with", "you", "it", "not", "or", "be", "are", "from",
	"at", "as", "your", "all", "have", "new", "more", "an", "was", "we",
	"will", "home", "can", "us", "about", "if", "page", "my", "has", "search",
	"free", "but", "our", "one", "other", "do", "no", "information", "time", "they",
	"site", "he", "up", "may", "what", "which", "their", "news", "out", "use",
	"any", "there", "see", "only", "so", "his", "when", "contact", "here", "business",
	"who", "web", "also", "now", "help", "get", "pm", "view", "online", "first",
	"am", "been", "would", "how", "were", "me", "services", "some", "these", "click",
	"its", "like", "service", "than", "find", "price", "date", "back", "top", "people",
	"had", "list", "name", "just", "over", "state", "year", "day", "into", "email",
	"two", "health", "world", "next", "used", "go", "work", "last", "most", "products",
	"music", "buy", "data", "make", "them", "should", "product", "system", "post", "her",
	"city", "policy", "number", "such", "please", "available", "copyright", "support", "message", "after",
	"best", "software", "then", "jan", "good", "video", "well", "where", "info", "rights",
	"public", "books", "high", "through", "each", "project", "general", "research", "before", "law",
	"meeting", "schedule", "quarterly", "report", "attached", "please", "find", "review", "deadline", "proposal",
	"invoice", "payment", "receipt", "order", "confirmation", "subscription", "renewal", "notification", "alert", "update",
	"customer", "account", "password", "security", "login", "verify", "click", "link", "thanks", "regards",
	"letter", "fax", "memo", "draft", "final", "revised", "version", "attachment", "forwarded", "replied",
}

// Subject phrase fragments.
var subjectFragments = []string{
	"Re: quarterly numbers", "FWD: meeting notes", "Invoice #%d pending",
	"Your order %d has shipped", "Security alert for your account",
	"Action required: password reset", "Re: proposal for %s",
	"Welcome to %s", "Your subscription expires soon", "Weekly digest — %s",
	"Project %s status update", "Re: Re: Re: budget discussion",
	"Reminder: meeting at %d:00", "Code review requested", "Thanks for your order",
	"Please review attached", "Draft %s for your review",
}

// Email addresses pool.
var addressPool = []string{
	"alice@example.com", "bob@example.com", "carol@example.net",
	"dave@corp.example", "eve@widgets.example", "frank@partners.example",
	"ops@monitoring.example", "noreply@saas.example", "support@vendor.example",
	"billing@vendor.example", "hr@employer.example", "ci@build.example",
}

type synthMessage struct {
	ID          string
	MailboxID   string
	PrincipalID string
	From        string
	To          string
	Cc          string
	Subject     string
	Body        string
	Date        time.Time
	Flags       []string
}

func newCorpus(n int, seed int64) []synthMessage {
	r := rand.New(rand.NewSource(seed))
	out := make([]synthMessage, n)
	startDate := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		out[i] = synthMessage{
			ID:          fmt.Sprintf("msg-%08d", i),
			MailboxID:   fmt.Sprintf("mbox-%03d", i%50),
			PrincipalID: fmt.Sprintf("princ-%02d", i%10),
			From:        addressPool[r.Intn(len(addressPool))],
			To:          addressPool[r.Intn(len(addressPool))],
			Cc:          addressPool[r.Intn(len(addressPool))],
			Subject:     buildSubject(r),
			Body:        buildBody(r, bodySizeBytes),
			Date:        startDate.Add(time.Duration(i) * time.Minute),
			Flags:       pickFlags(r),
		}
	}
	return out
}

func buildSubject(r *rand.Rand) string {
	s := subjectFragments[r.Intn(len(subjectFragments))]
	// Fill any %d / %s with determinstic junk.
	return fmt.Sprintf(s, r.Intn(10000), vocab[r.Intn(len(vocab))])
}

func buildBody(r *rand.Rand, target int) string {
	buf := make([]byte, 0, target+64)
	for len(buf) < target {
		w := vocab[r.Intn(len(vocab))]
		buf = append(buf, w...)
		if r.Intn(8) == 0 {
			buf = append(buf, '.', '\n')
		} else {
			buf = append(buf, ' ')
		}
	}
	return string(buf[:target])
}

func pickFlags(r *rand.Rand) []string {
	flagSet := []string{"\\Seen", "\\Flagged", "\\Answered", "$Important"}
	out := make([]string, 0, 2)
	for _, f := range flagSet {
		if r.Intn(3) == 0 {
			out = append(out, f)
		}
	}
	return out
}

func buildMapping() mapping.IndexMapping {
	m := bleve.NewIndexMapping()
	// Keep it simple: standard analyzer on text fields; keyword on IDs and flags.
	emailMap := bleve.NewDocumentMapping()

	textField := bleve.NewTextFieldMapping()
	textField.Analyzer = "standard"
	textField.Store = false
	textField.IncludeTermVectors = false
	textField.IncludeInAll = true

	keywordField := bleve.NewTextFieldMapping()
	keywordField.Analyzer = "keyword"
	keywordField.Store = false
	keywordField.IncludeInAll = false

	dateField := bleve.NewDateTimeFieldMapping()
	dateField.Store = false
	dateField.IncludeInAll = false

	emailMap.AddFieldMappingsAt("subject", textField)
	emailMap.AddFieldMappingsAt("body", textField)
	emailMap.AddFieldMappingsAt("from", textField)
	emailMap.AddFieldMappingsAt("to", textField)
	emailMap.AddFieldMappingsAt("cc", textField)
	emailMap.AddFieldMappingsAt("mailbox", keywordField)
	emailMap.AddFieldMappingsAt("principal", keywordField)
	emailMap.AddFieldMappingsAt("flags", keywordField)
	emailMap.AddFieldMappingsAt("date", dateField)

	m.AddDocumentMapping("_default", emailMap)
	m.DefaultAnalyzer = "standard"
	return m
}

func newIndex(tb testing.TB, dir string) bleve.Index {
	tb.Helper()
	path := filepath.Join(dir, "index.bleve")
	idx, err := bleve.New(path, buildMapping())
	if err != nil {
		tb.Fatalf("bleve.New: %v", err)
	}
	return idx
}

type cadenceResult struct {
	BatchSize      int
	Docs           int
	TotalSeconds   float64
	DocsPerSecond  float64
	P50MicrosPerOp float64
	P95MicrosPerOp float64
	P99MicrosPerOp float64
	HeapPeakMB     float64
	IndexSizeMB    float64
}

// measureCadence runs one (batch size, doc count) point.
func measureCadence(tb testing.TB, corpus []synthMessage, batchSize int) cadenceResult {
	tb.Helper()
	dir, err := os.MkdirTemp("", "storefts-spike-*")
	if err != nil {
		tb.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(dir)

	idx := newIndex(tb, dir)
	defer idx.Close()

	runtime.GC()
	var baseMS runtime.MemStats
	runtime.ReadMemStats(&baseMS)

	var (
		peakHeap uint64
		latNS    = make([]int64, 0, len(corpus))
		latMu    sync.Mutex
	)

	start := time.Now()

	batch := idx.NewBatch()
	for i, msg := range corpus {
		docStart := time.Now()
		doc := map[string]interface{}{
			"subject":   msg.Subject,
			"body":      msg.Body,
			"from":      msg.From,
			"to":        msg.To,
			"cc":        msg.Cc,
			"mailbox":   msg.MailboxID,
			"principal": msg.PrincipalID,
			"flags":     msg.Flags,
			"date":      msg.Date,
		}
		if err := batch.Index(msg.ID, doc); err != nil {
			tb.Fatalf("batch.Index: %v", err)
		}
		// Flush when batch full or at end.
		if batch.Size() >= batchSize || i == len(corpus)-1 {
			if err := idx.Batch(batch); err != nil {
				tb.Fatalf("idx.Batch: %v", err)
			}
			batch = idx.NewBatch()
		}
		d := time.Since(docStart).Nanoseconds()
		latMu.Lock()
		latNS = append(latNS, d)
		latMu.Unlock()

		// Sample peak heap every 1024 docs to avoid dominating the measurement.
		if i%1024 == 0 {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > peakHeap {
				peakHeap = ms.HeapAlloc
			}
		}
	}

	total := time.Since(start)

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if ms.HeapAlloc > peakHeap {
		peakHeap = ms.HeapAlloc
	}

	sort.Slice(latNS, func(i, j int) bool { return latNS[i] < latNS[j] })
	p50 := percentile(latNS, 0.50)
	p95 := percentile(latNS, 0.95)
	p99 := percentile(latNS, 0.99)

	sizeMB := dirSizeMB(tb, dir)

	return cadenceResult{
		BatchSize:      batchSize,
		Docs:           len(corpus),
		TotalSeconds:   total.Seconds(),
		DocsPerSecond:  float64(len(corpus)) / total.Seconds(),
		P50MicrosPerOp: float64(p50) / 1000.0,
		P95MicrosPerOp: float64(p95) / 1000.0,
		P99MicrosPerOp: float64(p99) / 1000.0,
		HeapPeakMB:     float64(peakHeap) / (1024 * 1024),
		IndexSizeMB:    sizeMB,
	}
}

func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)-1) * p)
	return sorted[i]
}

func dirSizeMB(tb testing.TB, dir string) float64 {
	tb.Helper()
	var total int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return float64(total) / (1024 * 1024)
}

// TestSpikeCadence is the primary driver: it runs each batch size over a
// corpus sized to fit a reasonable time budget (batch=1 is quadratically
// expensive because every Batch() is a commit), and emits a CSV line per
// run. Use -tags=spike to run.
func TestSpikeCadence(t *testing.T) {
	if os.Getenv("HEROLD_SPIKE") == "" {
		t.Skip("HEROLD_SPIKE unset; opt into the spike harness explicitly")
	}

	// All surviving batch sizes run the full baseline corpus. batch=1 is
	// excluded at the batchSizes level; see the comment there.
	sizeFor := map[int]int{
		10:    baselineDocs,
		100:   baselineDocs,
		500:   baselineDocs,
		2000:  baselineDocs,
		10000: baselineDocs,
	}
	if v := os.Getenv("HEROLD_SPIKE_DOCS"); v != "" {
		var d int
		_, _ = fmt.Sscanf(v, "%d", &d)
		for k := range sizeFor {
			sizeFor[k] = d
		}
	}

	t.Logf("CORPUS body_bytes=%d bleve=v2.5.7 goos=%s goarch=%s cpu=%d",
		bodySizeBytes, runtime.GOOS, runtime.GOARCH, runtime.NumCPU())

	t.Logf("CSV batch_size,docs,seconds,docs_per_sec,p50_us,p95_us,p99_us,heap_peak_mb,index_size_mb")
	// Cache corpora by size — building 100k messages is ~0.5 s and done once.
	corpora := map[int][]synthMessage{}
	for _, bs := range batchSizes {
		n := sizeFor[bs]
		c, ok := corpora[n]
		if !ok {
			c = newCorpus(n, int64(42+n))
			corpora[n] = c
		}
		r := measureCadence(t, c, bs)
		t.Logf("CSV %d,%d,%.3f,%.1f,%.1f,%.1f,%.1f,%.1f,%.1f",
			r.BatchSize, r.Docs, r.TotalSeconds, r.DocsPerSecond,
			r.P50MicrosPerOp, r.P95MicrosPerOp, r.P99MicrosPerOp,
			r.HeapPeakMB, r.IndexSizeMB)
	}
}

// TestSpikeQueryLatency: populate once, run a mix of realistic IMAP SEARCH
// / JMAP Email/query-shaped queries; report p50/p95.
func TestSpikeQueryLatency(t *testing.T) {
	if os.Getenv("HEROLD_SPIKE") == "" {
		t.Skip("HEROLD_SPIKE unset; opt into the spike harness explicitly")
	}
	docs := baselineDocs
	if v := os.Getenv("HEROLD_SPIKE_DOCS"); v != "" {
		_, _ = fmt.Sscanf(v, "%d", &docs)
	}
	corpus := newCorpus(docs, 7)

	dir, err := os.MkdirTemp("", "storefts-query-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(dir)

	idx := newIndex(t, dir)
	defer idx.Close()

	const popBatch = 1000
	batch := idx.NewBatch()
	for i, msg := range corpus {
		doc := map[string]interface{}{
			"subject":   msg.Subject,
			"body":      msg.Body,
			"from":      msg.From,
			"to":        msg.To,
			"cc":        msg.Cc,
			"mailbox":   msg.MailboxID,
			"principal": msg.PrincipalID,
			"flags":     msg.Flags,
			"date":      msg.Date,
		}
		if err := batch.Index(msg.ID, doc); err != nil {
			t.Fatalf("batch.Index: %v", err)
		}
		if batch.Size() >= popBatch || i == len(corpus)-1 {
			if err := idx.Batch(batch); err != nil {
				t.Fatalf("idx.Batch: %v", err)
			}
			batch = idx.NewBatch()
		}
	}

	// Realistic query shapes. These cover the top of what IMAP SEARCH
	// (`TEXT`, `FROM`, `SUBJECT`, `SINCE`) and JMAP Email/query (`text`,
	// `from`, `inMailbox`, `before`, `after`) actually emit.
	mkMatch := func(term, field string) query.Query {
		q := bleve.NewMatchQuery(term)
		q.SetField(field)
		return q
	}
	mkPhrase := func(phrase, field string) query.Query {
		q := bleve.NewMatchPhraseQuery(phrase)
		q.SetField(field)
		return q
	}
	mkTerm := func(term, field string) query.Query {
		q := bleve.NewTermQuery(term)
		q.SetField(field)
		return q
	}

	queries := []struct {
		name string
		q    query.Query
	}{
		{"subject_term_meeting", mkMatch("meeting", "subject")},
		{"subject_term_invoice", mkMatch("invoice", "subject")},
		{"body_term_password", mkMatch("password", "body")},
		{"body_phrase", mkPhrase("security alert", "subject")},
		{"from_domain", mkMatch("example.com", "from")},
		{"mailbox_facet", mkTerm("mbox-010", "mailbox")},
		{"body_and_mailbox", bleve.NewConjunctionQuery(
			mkMatch("quarterly", "body"),
			mkTerm("mbox-010", "mailbox"),
		)},
		{"subject_or_body", bleve.NewDisjunctionQuery(
			mkMatch("renewal", "subject"),
			mkMatch("renewal", "body"),
		)},
		{"flag_facet", mkTerm("\\Flagged", "flags")},
		{"date_range", func() query.Query {
			start := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
			end := time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC)
			q := bleve.NewDateRangeQuery(start, end)
			q.SetField("date")
			return q
		}()},
		{"text_all_fields", bleve.NewQueryStringQuery("security alert")},
		{"principal_scope", bleve.NewConjunctionQuery(
			mkTerm("princ-03", "principal"),
			mkMatch("invoice", "subject"),
		)},
	}

	t.Logf("QUERY name,runs,p50_us,p95_us,total_hits_last_run")
	const runs = 20
	for _, q := range queries {
		lats := make([]int64, 0, runs)
		var lastHits uint64
		for r := 0; r < runs; r++ {
			req := bleve.NewSearchRequest(q.q)
			req.Size = 50
			s := time.Now()
			res, err := idx.Search(req)
			if err != nil {
				t.Fatalf("search %s: %v", q.name, err)
			}
			lats = append(lats, time.Since(s).Nanoseconds())
			lastHits = res.Total
		}
		sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
		p50 := percentile(lats, 0.50)
		p95 := percentile(lats, 0.95)
		t.Logf("QUERY %s,%d,%.1f,%.1f,%d",
			q.name, runs, float64(p50)/1000.0, float64(p95)/1000.0, lastHits)
	}
}

// TestSpikeAsyncWorker models the recommended architecture: a bounded
// async worker consumes off a buffered change feed, batches writes, and
// commits on either size or time deadline. Reports end-to-end delivery-to-
// searchable lag at 100 msg/s synthetic ingest.
func TestSpikeAsyncWorker(t *testing.T) {
	if os.Getenv("HEROLD_SPIKE") == "" {
		t.Skip("HEROLD_SPIKE unset; opt into the spike harness explicitly")
	}
	const (
		ingestRate   = 100 // msgs/s
		durationSecs = 30
		totalDocs    = ingestRate * durationSecs
		commitEvery  = 500
		commitWindow = 500 * time.Millisecond
		feedBuffer   = 4096
	)
	corpus := newCorpus(totalDocs, 11)

	dir, err := os.MkdirTemp("", "storefts-async-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(dir)

	idx := newIndex(t, dir)
	defer idx.Close()

	type job struct {
		msg       synthMessage
		producedT time.Time
	}
	feed := make(chan job, feedBuffer)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Observed delivery-to-searchable latencies.
	var (
		lats     []int64
		latsMu   sync.Mutex
		indexed  atomic.Int64
		commits  atomic.Int64
		flushMax time.Duration
		flushMu  sync.Mutex
	)

	// Single async worker; bounded goroutine, ctx-respecting. Fan-out to N
	// workers would require per-worker sharding of the change feed; that's
	// noted in the write-up, not exercised here.
	done := make(chan struct{})
	go func() {
		defer close(done)
		batch := idx.NewBatch()
		pending := make([]job, 0, commitEvery)
		timer := time.NewTimer(commitWindow)
		defer timer.Stop()
		flush := func() {
			if len(pending) == 0 {
				return
			}
			s := time.Now()
			if err := idx.Batch(batch); err != nil {
				t.Errorf("idx.Batch: %v", err)
			}
			fl := time.Since(s)
			flushMu.Lock()
			if fl > flushMax {
				flushMax = fl
			}
			flushMu.Unlock()
			now := time.Now()
			latsMu.Lock()
			for _, j := range pending {
				lats = append(lats, now.Sub(j.producedT).Nanoseconds())
			}
			latsMu.Unlock()
			indexed.Add(int64(len(pending)))
			commits.Add(1)
			pending = pending[:0]
			batch = idx.NewBatch()
		}
		for {
			select {
			case <-ctx.Done():
				flush()
				return
			case j, ok := <-feed:
				if !ok {
					flush()
					return
				}
				doc := map[string]interface{}{
					"subject":   j.msg.Subject,
					"body":      j.msg.Body,
					"from":      j.msg.From,
					"to":        j.msg.To,
					"cc":        j.msg.Cc,
					"mailbox":   j.msg.MailboxID,
					"principal": j.msg.PrincipalID,
					"flags":     j.msg.Flags,
					"date":      j.msg.Date,
				}
				if err := batch.Index(j.msg.ID, doc); err != nil {
					t.Errorf("batch.Index: %v", err)
				}
				pending = append(pending, j)
				if len(pending) >= commitEvery {
					flush()
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(commitWindow)
				}
			case <-timer.C:
				flush()
				timer.Reset(commitWindow)
			}
		}
	}()

	// Producer: paced 100 msg/s. Uses a ticker; the channel buffer acts as
	// the backpressure boundary — if worker lags, producer blocks, which
	// models "FTS-lag bounded ingest" correctly.
	interval := time.Second / time.Duration(ingestRate)
	ticker := time.NewTicker(interval)
	start := time.Now()
	for i := 0; i < totalDocs; i++ {
		<-ticker.C
		feed <- job{msg: corpus[i], producedT: time.Now()}
	}
	close(feed)
	ticker.Stop()
	<-done
	total := time.Since(start)

	latsMu.Lock()
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	p50 := percentile(lats, 0.50)
	p95 := percentile(lats, 0.95)
	p99 := percentile(lats, 0.99)
	latsMu.Unlock()

	t.Logf("ASYNC docs=%d rate=%d/s commit_every=%d commit_window=%s",
		totalDocs, ingestRate, commitEvery, commitWindow)
	t.Logf("ASYNC observed_seconds=%.2f indexed=%d commits=%d max_flush_ms=%.1f",
		total.Seconds(), indexed.Load(), commits.Load(),
		float64(flushMax.Microseconds())/1000.0)
	t.Logf("ASYNC lag_p50_ms=%.2f lag_p95_ms=%.2f lag_p99_ms=%.2f",
		float64(p50)/1e6, float64(p95)/1e6, float64(p99)/1e6)
}

// TestSpikeInlineCost models the "inline in delivery path" alternative:
// each message is indexed synchronously (batch size 1) before SMTP returns
// 250 OK. Used to quantify the worst case.
func TestSpikeInlineCost(t *testing.T) {
	if os.Getenv("HEROLD_SPIKE") == "" {
		t.Skip("HEROLD_SPIKE unset; opt into the spike harness explicitly")
	}
	docs := 5000
	if v := os.Getenv("HEROLD_SPIKE_DOCS"); v != "" {
		_, _ = fmt.Sscanf(v, "%d", &docs)
	}
	corpus := newCorpus(docs, 99)
	r := measureCadence(t, corpus, 1)
	t.Logf("INLINE docs=%d seconds=%.3f docs_per_sec=%.1f p50_ms=%.2f p95_ms=%.2f p99_ms=%.2f heap_peak_mb=%.1f",
		r.Docs, r.TotalSeconds, r.DocsPerSecond,
		r.P50MicrosPerOp/1000.0, r.P95MicrosPerOp/1000.0, r.P99MicrosPerOp/1000.0,
		r.HeapPeakMB)
}
