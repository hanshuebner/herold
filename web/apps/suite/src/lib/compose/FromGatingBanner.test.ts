/**
 * FromGatingBanner (re #545): the banner names the actual external
 * submission condition, links to the identity settings and offers the
 * connection test.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import FromGatingBanner from './FromGatingBanner.svelte';
import { en } from '../i18n/en';
import { de } from '../i18n/de';

const { testSubmission, refresh } = vi.hoisted(() => ({
  testSubmission: vi.fn(),
  refresh: vi.fn(async () => undefined),
}));

vi.mock('../api/identity-submission', () => ({ testSubmission }));
vi.mock('../identities/identity-submission.svelte', () => ({
  submissionStore: { forIdentity: vi.fn(() => ({ refresh })) },
}));
vi.mock('../i18n/i18n.svelte', async () => {
  const { en } = await import('../i18n/en');
  return {
    t: (key: string, params?: Record<string, string>): string => {
      let s = (en as Record<string, string>)[key] ?? key;
      for (const [k, v] of Object.entries(params ?? {})) s = s.replace(`{${k}}`, v);
      return s;
    },
  };
});

beforeEach(() => {
  testSubmission.mockReset();
  refresh.mockClear();
});

describe('FromGatingBanner', () => {
  it('states auth-failed, unreachable and not-probed differently', () => {
    const texts = (['external-auth-failed', 'external-unreachable', 'external-unverified'] as const).map(
      (reason) => {
        const { container, unmount } = render(FromGatingBanner, { identityId: '7', reason });
        const txt = container.querySelector('[data-testid="from-gating-message"]')?.textContent;
        unmount();
        return txt;
      },
    );
    expect(new Set(texts).size).toBe(3);
    expect(texts[0]).toMatch(/rejected the stored credentials/);
    expect(texts[1]).toMatch(/could not be reached/);
    expect(texts[2]).toMatch(/not been verified/);
  });

  it('links to the identity submission settings', () => {
    const { getByTestId } = render(FromGatingBanner, {
      identityId: '7',
      reason: 'external-auth-failed',
    });
    expect(getByTestId('from-gating-settings-link').getAttribute('href')).toBe(
      '#/settings/identities/7',
    );
  });

  it('runs the connection test and refreshes the submission handle', async () => {
    testSubmission.mockResolvedValue({ ok: false, detail: '535 Incorrect authentication data' });
    const { getByTestId } = render(FromGatingBanner, {
      identityId: '7',
      reason: 'external-auth-failed',
    });
    await fireEvent.click(getByTestId('from-gating-test'));
    await waitFor(() => expect(getByTestId('from-gating-result')).toBeInTheDocument());
    expect(testSubmission).toHaveBeenCalledWith('7');
    expect(refresh).toHaveBeenCalled();
    expect(getByTestId('from-gating-result').textContent).toMatch(/535 Incorrect/);
  });

  it('offers no retest or link for non-external reasons', () => {
    const { queryByTestId } = render(FromGatingBanner, { identityId: '7', reason: 'unverified' });
    expect(queryByTestId('from-gating-test')).toBeNull();
    expect(queryByTestId('from-gating-settings-link')).toBeNull();
  });

  it('keeps en and de keys in step', () => {
    const keys = Object.keys(en).filter((k) => k.startsWith('compose.from.sendDisabled.'));
    for (const k of keys) expect(Object.keys(de)).toContain(k);
    expect(keys).toContain('compose.from.sendDisabled.authFailed');
  });
});
