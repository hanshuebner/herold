/**
 * Regression test: hosted identity is not blocked after settings visit (re #107).
 *
 * Sequence that triggered the bug:
 *   1. Alice opens compose directly -> Send is enabled (submissionStore empty;
 *      submissionResolver returns null; isExternalWithoutSubmission(id, null)=false).
 *   2. Alice visits Settings / Account. SettingsView.$effect loads the submission
 *      status for every identity in mail.identities via submissionStore.load().
 *   3. Alice opens compose again. submissionStore now has data; submissionResolver
 *      returns a SubmissionSummary. If domain_authoritative is false in that data,
 *      isExternalWithoutSubmission returns true and Send is disabled.
 *
 * Root cause: GET /api/v1/identities/default/submission did not set
 * domain_authoritative: true for the synthetic default identity even when the
 * caller's domain IS authoritative on this server. The test pins the expected
 * behavior: after the settings visit populates the store, composeFromGating must
 * still allow Send for a hosted identity.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { composeFromGating } from './from-picker';
import type { Identity } from '../mail/types';
import type { SubmissionSummary } from '../identities/identity-status';

// Mock the JMAP sync handler so the store never touches the push channel.
vi.mock('../jmap/sync.svelte', () => ({
  sync: {
    on: vi.fn(() => () => {
      /* no-op unsubscribe */
    }),
  },
}));

// Mock the REST wrapper; each test drives this independently.
vi.mock('../api/identity-submission', () => ({
  getSubmission: vi.fn(),
}));

const { getSubmission } = await import('../api/identity-submission');
const { submissionStore } = await import('../identities/identity-submission.svelte');

/**
 * The synthetic default identity Alice sees on a fresh herold instance:
 *   id = "default"   (the per-principal synthetic id the server emits)
 *   email = alice@example.local (hosted domain)
 *   verifiedAt = non-null (synthetic default is verified-by-construction)
 */
const ALICE_DEFAULT: Identity = {
  id: 'default',
  name: 'Alice',
  email: 'alice@example.local',
  replyTo: null,
  bcc: null,
  textSignature: '',
  htmlSignature: '',
  mayDelete: false,
  verifiedAt: '2024-01-01T00:00:00Z',
  isDefault: true,
};

/**
 * Replicate the mapping ComposeWindow.submissionResolver applies to
 * the raw SubmissionStatus data.  When data is null (store not yet loaded)
 * the resolver returns null; once loaded it maps the fields.
 */
function resolveFromStore(id: string): SubmissionSummary | null {
  const data = submissionStore.forIdentity(id).data;
  if (!data) return null;
  return {
    configured: data.configured === true,
    state: data.state ?? null,
    domainAuthoritative: data.domain_authoritative,
  };
}

describe('compose gating: hosted identity after settings visit (re #107)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    submissionStore.evict('default');
  });

  it('allows Send for the synthetic default identity when no submission data is loaded yet', () => {
    // Fresh compose: store empty, submissionResolver returns null.
    // isExternalWithoutSubmission(alice, null) -> false -> allowed.
    const sub = resolveFromStore('default');
    expect(sub).toBeNull();

    const gating = composeFromGating(ALICE_DEFAULT, sub);
    expect(gating.allowed).toBe(true);
    expect(gating.reason).toBeNull();
  });

  it('allows Send for the synthetic default identity after settings page loads its submission status (hosted domain)', async () => {
    // Simulate GET /api/v1/identities/default/submission as the fixed backend
    // returns it: domain_authoritative is true because alice@example.local is on
    // a locally-hosted domain.  The pre-fix backend omitted DomainAuthoritative
    // in the synthetic-default path, so Go's zero-value produced false, which
    // caused isExternalWithoutSubmission to return true and disabled Send.
    vi.mocked(getSubmission).mockResolvedValueOnce({
      configured: false,
      domain_authoritative: true,
      available_oauth_providers: [],
    });

    // Simulate SettingsView.$effect: load() for every identity in the list.
    await submissionStore.forIdentity('default').load();

    // Store is now populated; ComposeWindow.submissionResolver returns a summary.
    const sub = resolveFromStore('default');
    expect(sub).not.toBeNull();

    const gating = composeFromGating(ALICE_DEFAULT, sub);
    expect(gating.allowed).toBe(true);
    expect(gating.reason).toBeNull();
  });
});
