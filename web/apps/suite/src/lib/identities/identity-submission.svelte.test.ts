/**
 * Regression tests for IdentitySubmissionStore._entry.
 *
 * Pins the fix in commit 27102c4 ("suite/identity-submission: make _entry
 * pure-read; stop mutating during render"). Before the fix, _entry lazily
 * created a fresh idle entry when none was cached and wrote it back to
 * #entries. When that path was reached from a Svelte 5 reactive read
 * context (e.g. a $derived in SettingsView's render pipeline reading
 * handle.status / .data / .error), Svelte 5 raised state_unsafe_mutation
 * and the App.svelte error boundary blanked the page on /settings.
 *
 * Four invariants are pinned here:
 *
 *   A. _entry is a pure read for an unseen identity: the underlying
 *      Map's identity does not change. We probe this via _patch (which
 *      always allocates a new Map): a second _patch after several
 *      _entry calls sees a Map whose size is exactly 1, proving the
 *      intermediate _entry calls did not populate the cache.
 *   B. The default returned for two distinct unseen ids is the same
 *      reference (the shared sentinel) and has the idle/null/null shape.
 *      Reading both does not expand the cache.
 *   C. Reading handle.status from inside a Svelte 5 $derived (which is
 *      one of the reactive contexts that triggers state_unsafe_mutation
 *      on a write) does not throw. This is the direct repro of the bug:
 *      on the pre-fix code, $derived(() => handle.status) calls _entry,
 *      _entry writes #entries, and Svelte throws state_unsafe_mutation.
 *      We use $effect.root + $derived + flushSync to observe synchronously.
 *   D. After .load() resolves, the entry is in #entries and subsequent
 *      _entry returns the loaded value, not the shared default. This
 *      pins the load-path side of the fix (writes are routed through
 *      _patch, which is what actually grows the cache).
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { flushSync } from 'svelte';

// ── Mock the REST wrapper before the dynamic import ───────────────────────

vi.mock('../api/identity-submission', () => ({
  getSubmission: vi.fn(),
}));

// ── Mock the JMAP sync registration so the store does not touch a real
//    push channel. The store calls sync.on('Identity', handler) once on
//    the first forIdentity() call and stashes the unsubscribe; we just
//    need the call to be a no-op that returns a disposer.

vi.mock('../jmap/sync.svelte', () => ({
  sync: {
    on: vi.fn(() => () => {
      /* no-op unsubscribe */
    }),
  },
}));

// ── Resolve mock handles after the mocks are wired ────────────────────────

const { getSubmission } = await import('../api/identity-submission');
const { submissionStore } = await import('./identity-submission.svelte');

// ── Helpers ───────────────────────────────────────────────────────────────

/**
 * Run a body inside a transient $effect.root so $derived / $effect are
 * legal, then dispose. Returns whatever the body returns. Any error
 * thrown synchronously inside the body (including state_unsafe_mutation
 * raised by Svelte when state is written from a $derived) propagates.
 */
function withReactiveRoot<T>(body: () => T): T {
  let result!: T;
  const stop = $effect.root(() => {
    result = body();
  });
  stop();
  return result;
}

// ── Tests ─────────────────────────────────────────────────────────────────

describe('IdentitySubmissionStore._entry — state_unsafe_mutation regression', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Each test runs against the module singleton; evict any entries
    // a previous test may have loaded so the unseen-id assertions hold.
    submissionStore.evict('id-a');
    submissionStore.evict('id-b');
    submissionStore.evict('id-c');
    submissionStore.evict('id-loaded');
  });

  it('(A) _entry for an unseen id does not mutate the entries cache', () => {
    // Read the handle (and thus _entry) several times for unseen ids.
    // None of these reads should populate #entries. We probe the
    // cache indirectly via _entry's return-by-reference shape: after
    // the reads, _entry('id-a') must STILL return the shared default
    // sentinel — same reference as _entry('id-b'). On the pre-fix
    // code, each read of an unseen id wrote a fresh entry into
    // #entries, so two reads of different ids would return two
    // distinct allocated objects.
    const handleA = submissionStore.forIdentity('id-a');
    const handleB = submissionStore.forIdentity('id-b');

    // Drive several reactive reads (the bug surface).
    void handleA.status;
    void handleA.data;
    void handleA.error;
    void handleB.status;
    void handleB.data;

    // The shared-default sentinel: both ids must still resolve to
    // the same object. On the pre-fix code these would be distinct
    // freshly-allocated entries that had been written into #entries.
    const entryA = submissionStore._entry('id-a');
    const entryB = submissionStore._entry('id-b');
    expect(entryA).toBe(entryB);

    // And patching a third id leaves the prior two untouched —
    // they still resolve to the same shared default, because the
    // bare reads above never put them in the cache.
    submissionStore._patch('id-c', { status: 'ready' });
    expect(submissionStore._entry('id-a')).toBe(entryA);
    expect(submissionStore._entry('id-b')).toBe(entryB);
  });

  it('(B) two unseen ids share the same default-entry reference', () => {
    const entryA = submissionStore._entry('id-a');
    const entryB = submissionStore._entry('id-b');

    // Shared sentinel: same object identity.
    expect(entryA).toBe(entryB);

    // Shape: idle / null / null — never a configured submission.
    expect(entryA).toEqual({ status: 'idle', data: null, error: null });

    // And neither read expanded the cache: a subsequent _patch into a
    // third id leaves the prior two still at the shared default.
    submissionStore._patch('id-c', { status: 'loading' });
    expect(submissionStore._entry('id-a')).toBe(entryA);
    expect(submissionStore._entry('id-b')).toBe(entryB);
  });

  it('(C) reading handle.status from a $derived does not throw state_unsafe_mutation', () => {
    // This is the exact shape that broke /settings before the fix:
    // a $derived (SettingsView's render-time reactive read) calls
    // handle.status, which calls _entry, which (on the pre-fix code)
    // writes #entries. Svelte 5 raises state_unsafe_mutation on the
    // write — we capture any error raised during the derived's eval.
    //
    // On the fixed code, _entry returns the shared default with no
    // write, and the derived produces 'idle' without error.

    let captured: unknown = null;
    let derivedValue: string | undefined;

    try {
      withReactiveRoot(() => {
        const handle = submissionStore.forIdentity('id-a');
        const status = $derived(handle.status);
        // Touch the derived inside an effect so it actually evaluates.
        $effect(() => {
          derivedValue = status;
        });
        flushSync();
      });
    } catch (err) {
      captured = err;
    }

    expect(captured).toBeNull();
    expect(derivedValue).toBe('idle');
  });

  it('(D) after load() resolves, _entry returns the loaded value', async () => {
    const status = {
      configured: true,
      submit_host: 'smtp.example.com',
      submit_port: 587,
      submit_security: 'starttls' as const,
      submit_auth_method: 'password' as const,
      state: 'ok' as const,
    };
    vi.mocked(getSubmission).mockResolvedValueOnce(status);

    const handle = submissionStore.forIdentity('id-loaded');

    // Before load: shared default.
    const before = submissionStore._entry('id-loaded');
    expect(before.status).toBe('idle');
    expect(before.data).toBeNull();

    await handle.load();

    // After load: cached entry distinct from the shared default,
    // carrying the API payload and 'ready' status.
    const after = submissionStore._entry('id-loaded');
    expect(after.status).toBe('ready');
    expect(after.data).toEqual(status);
    expect(after.error).toBeNull();
    // Not the shared default any more.
    expect(after).not.toBe(before);

    // Handle getters reflect the same loaded state.
    expect(handle.status).toBe('ready');
    expect(handle.data).toEqual(status);
    expect(handle.error).toBeNull();

    // The mock was hit exactly once.
    expect(getSubmission).toHaveBeenCalledTimes(1);
    expect(getSubmission).toHaveBeenCalledWith('id-loaded');
  });
});
