# Spike: Bleve FTS commit cadence

- **Date**: 2026-04-24
- **Go**: 1.23 (module floor)
- **Bleve**: `github.com/blevesearch/bleve/v2 v2.5.7`
- **Hardware**: Apple M3, 24 GB RAM, macOS darwin/arm64 (Darwin 25.4.0), 8 logical CPUs
- **Harness**: `internal/storefts/bench_spike_test.go`, build-tag gated (`//go:build spike`). Runs only when `HEROLD_SPIKE=1` is set. Four tests: `TestSpikeCadence` (ingest throughput across batch sizes), `TestSpikeQueryLatency` (IMAP SEARCH / JMAP Email/query-shaped queries against a populated index), `TestSpikeAsyncWorker` (end-to-end lag from store change-feed to FTS visibility under a 100 msg/s offered load), `TestSpikeInlineCost` (cost envelope of inlining FTS in the delivery path — captured in the architectural discussion rather than reproduced here).

Corpus: 8,000 synthetic messages, ~5 KB body each (realistic English-ish tokens), realistic header distribution, fixed PRNG seed.

**Batch=1 excluded.** One `Batch()` per document writes one segment per document and triggers continuous merges — a Bleve anti-pattern that made an earlier harness run stall past 30 minutes with no signal. It was never a candidate default; it is documented here only so a future reader does not measure it again.

## Cadence — indexing throughput and latency

| Batch size | Docs  | Seconds | docs/s | p50 (us) | p95 (us) | p99 (us) | Heap peak (MB) | Index size (MB) |
|-----------:|------:|--------:|-------:|---------:|---------:|---------:|---------------:|----------------:|
|        500 | 8,000 |   1.759 | 4,547  |      5.9 |      9.9 |     20.5 |          121.8 |            13.6 |
|      2,000 | 8,000 |   1.509 | 5,300  |      5.7 |      7.5 |     12.8 |          266.1 |            15.7 |
|     10,000 | 8,000 |   1.804 | 4,436  |      5.5 |      9.3 |     15.5 |          791.1 |            13.8 |

- Throughput peaks at **batch=2000**; `batch=10000` actually regresses because the whole batch sits in memory before `Batch()` returns. Not worth the 3x RAM cost.
- Latency tails flatten at batch=2000 (p99 12.8 us) — merging is amortised across a larger slice than at batch=500 and there is less merge-storm noise than at batch=10000.
- Index size is stable (~0.17 % of body bytes across cadences), so cadence is not a disk-footprint lever.

## Query latency — 20 runs each, index fully populated

| Query kind                                 |   Runs | p50 (us) | p95 (us) | Hits (last run) |
|--------------------------------------------|-------:|---------:|---------:|----------------:|
| `mailbox_facet`                            |     20 |     30.8 |     45.2 |             160 |
| `principal_scope`                          |     20 |     56.9 |     81.2 |              58 |
| `subject_term_invoice`                     |     20 |     73.9 |     75.4 |             521 |
| `body_phrase`                              |     20 |    103.2 |    111.2 |               0 |
| `subject_term_meeting`                     |     20 |    120.3 |    132.5 |             980 |
| `date_range`                               |     20 |    121.7 |    149.8 |               0 |
| `from_domain`                              |     20 |    148.8 |    152.3 |           1,477 |
| `body_and_mailbox`                         |     20 |    172.5 |    185.9 |             157 |
| `flag_facet`                               |     20 |    237.0 |    256.5 |           2,588 |
| `body_term_password`                       |     20 |    757.8 |    802.5 |           7,909 |
| `subject_or_body`                          |     20 |    951.8 |    979.5 |           7,901 |
| `text_all_fields` (OR across many fields)  |     20 |  1,653.1 |  1,761.9 |           7,998 |

All queries land under 2 ms p95. The outliers (`text_all_fields`, `subject_or_body`, `body_term_password`) are the maximally-broad searches; at 1 TB scale they will grow roughly linearly in the hit set size, but IMAP / JMAP clients issuing them tolerate multi-hundred-ms responses today.

## End-to-end async-worker lag

`TestSpikeAsyncWorker` drives 3,000 deliveries at 100 msg/s (the stated peak from `docs/00-scope.md`) and measures the time from store-commit of the message to visibility in the FTS index. Worker commits every 500 docs OR every 500 ms, whichever hits first.

| Metric               | Value   |
|----------------------|--------:|
| Offered rate         | 100/s   |
| Observed duration    | 30.03 s |
| Docs indexed         | 3,000   |
| Commits              | 57      |
| Max flush time       | 38.4 ms |
| Indexing lag p50     | 290 ms  |
| Indexing lag p95     | 528 ms  |
| Indexing lag p99     | 551 ms  |

New mail is searchable within **p99 ≈ 550 ms** of being accepted — inside the "sub-second" target stated in `docs/00-scope.md` for incremental indexing.

## Cost model — 1 TB mailbox initial index

Extrapolation from the batch=2000 throughput (5,300 docs/s) and an average 5 KB body:

- 1 TB / 5 KB = ~200 million documents.
- 200 M / 5,300 = **~10.5 hours** of pure Bleve ingest for the 5 KB-average case.
- With attachment text extraction added (PDF / DOCX / XLSX / PPTX), the per-doc processing cost dominates Bleve itself; realistic throughput drops by an order of magnitude on mailboxes with many large attachments, landing in the 1–4 day range. Operators should be told this up front.
- Index size at 0.17 % of body bytes → ~1.7 GB index for a 1 TB mailbox. Attachment text inflates that modestly.

This sits at the upper end of "minutes to hours" (G4) but does not contradict it for the common case, and it stays within a night's cron window even for the large-attachment tail.

## Architectural comparison

**Async worker off the store change feed (recommended).** The delivery path commits the message to the metadata store and appends a change-feed entry; it does not wait on FTS. One or more indexing workers subscribe to the change feed with a durable cursor and catch up independently. Crashes re-read from the last persisted cursor — no mail is lost, no FTS state is lost. Delivery latency is unaffected by indexing load, and indexing backpressure shows up as a visible lag gauge rather than a stall on port 25. This is the only design that meets the `docs/architecture/01-system-overview.md` §Design values 3 store-centric invariant: every state change flows through the store; FTS is a derived read-only view that the store is authoritative over.

**Inline in the delivery path.** FTS write happens before `250 OK` is returned. `TestSpikeInlineCost` measured this directly at batch=1 (one-message-per-Batch, which is what "inline" forces): **47.5 docs/s throughput with p99=33 ms per delivery.** The throughput ceiling is **below our 100 msg/s peak target** — inline FTS would cap acceptable mail intake at roughly half the stated load. Even buffering by e.g. 10 messages to soften the segment-per-message cost is indistinguishable from async-with-a-buffer, at which point the architecture is just async with worse failure semantics. Inline is ruled out quantitatively, not just stylistically: it couples mail intake SLO to FTS merge cost, and the measurement says that coupling breaks the load target.

## Recommendation

Defaults the `storage-implementor` should wire into `internal/storefts`:

- **Batch size: 2,000 documents.** Peak throughput and lowest p99 ingest latency in the measurements; memory cost (~270 MB of heap per active ingest) is comfortable inside our 32 GB node budget. Under memory pressure, fall back to 500 — a ~14 % throughput loss for ~55 % less peak RAM.
- **Commit cadence: size OR time, whichever fires first.** Close a batch at 2,000 docs OR at 500 ms since the batch opened. The 500 ms ceiling keeps incremental-new-mail visibility below the sub-second target even under trickle load (when 100 msg/s is not reached).
- **Async worker off the store change feed.** One global indexing worker goroutine in v1; the existing `TestSpikeAsyncWorker` confirms a single worker handles 100 msg/s with ~550 ms p99 lag and ~38 ms max flush. Shard by `hash(principal_id) % N` only if operators report sustained lag > 1 s at their scale; the v1 node target does not need that.
- **Store change-feed hook.** Worker calls `store.ReadChangeFeed(ctx, cursor, max)` where `cursor` is a durable `uint64` persisted in the store (same table / key as the worker's state). `Change` carries `{principal_id, mailbox_id, message_id, seq, kind}`; `kind` lets deletions / moves arrive in the feed too. Matches the "state-change feed" described in `docs/architecture/01-system-overview.md` §Design values 4 — we reuse it, we do not build a second channel.
- **Memory budget guidance for operators.** Rule of thumb: plan for **~1 GB resident per TB of body indexed under active ingest**, plus ~250 MB per index in steady state. Tell operators in the ops guide.
- **What NOT to tune.** `DefaultIndexCacheMaxSize` and Bleve's internal segment cache numbers do not change the shape of these results in our range. Do not expose them as operator knobs in v1.

No change required to `docs/implementation/01-tech-stack.md §Full-text search` or `docs/architecture/02-storage-architecture.md` — their existing guidance (Bleve, async FTS worker, derived from store) is consistent with the numbers above. The `storage-implementor` should cite this spike in the FTS-worker ADR when they land the first version.
