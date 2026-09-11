/**
 * LLMInspectModal spam-verdict rendering (re #326).
 *
 * The spam classification record's confidence is absent/out-of-range for
 * verdict "unclassified" (classifier-failure rows written since 5f960d0a).
 * The modal must never compute a raw percentage from such a value
 * ("-100%" from the historical -1 sentinel, "NaN%" once the server omits
 * the field) -- it renders the verdict plus the reason text instead. A
 * genuine verdict with an in-range confidence still renders its
 * percentage.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, cleanup } from '@testing-library/svelte';
import type { MessageLLMInspect } from './transparency.svelte';

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
}));

let fetchInspectResult: MessageLLMInspect | null = null;

vi.mock('./transparency.svelte', () => ({
  llmTransparency: {
    loadStatus: 'ready',
    data: null,
    load: vi.fn(),
    fetchInspect: vi.fn(async () => fetchInspectResult),
  },
}));

import LLMInspectModal from './LLMInspectModal.svelte';

describe('LLMInspectModal spam verdict rendering (re #326)', () => {
  afterEach(() => {
    cleanup();
  });

  it('shows the verdict and reason, no percentage, for unclassified with a reason', async () => {
    fetchInspectResult = {
      emailId: 'email-1',
      spam: {
        verdict: 'unclassified',
        confidence: null,
        reason: 'plugin timed out after 5s',
        promptApplied: 'Classify the following email...',
        model: 'llama3.2',
        classifiedAt: '2026-09-01T12:00:00Z',
      },
    };

    render(LLMInspectModal, { props: { emailId: 'email-1', onClose: () => {} } });

    expect(await screen.findByText('unclassified')).toBeInTheDocument();
    expect(screen.getByText('plugin timed out after 5s')).toBeInTheDocument();
    expect(screen.queryByText(/%/)).not.toBeInTheDocument();
  });

  it('shows the percentage for a ham verdict with a 0.05 confidence', async () => {
    fetchInspectResult = {
      emailId: 'email-2',
      spam: {
        verdict: 'ham',
        confidence: 0.05,
        reason: 'Looks legitimate',
        promptApplied: 'Classify the following email...',
        model: 'llama3.2',
        classifiedAt: '2026-09-01T12:00:00Z',
      },
    };

    render(LLMInspectModal, { props: { emailId: 'email-2', onClose: () => {} } });

    expect(await screen.findByText('ham')).toBeInTheDocument();
    expect(screen.getByText('5%')).toBeInTheDocument();
  });

  it('suppresses the percentage when confidence is missing on a genuine verdict', async () => {
    fetchInspectResult = {
      emailId: 'email-3',
      spam: {
        verdict: 'spam',
        confidence: undefined,
        reason: 'Matches known spam pattern',
        promptApplied: 'Classify the following email...',
        model: 'llama3.2',
        classifiedAt: '2026-09-01T12:00:00Z',
      },
    };

    render(LLMInspectModal, { props: { emailId: 'email-3', onClose: () => {} } });

    expect(await screen.findByText('spam')).toBeInTheDocument();
    expect(screen.queryByText(/%/)).not.toBeInTheDocument();
  });
});
