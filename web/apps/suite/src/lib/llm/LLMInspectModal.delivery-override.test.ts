/**
 * LLMInspectModal delivery-override rendering (REQ-FILT-02a, issue #382).
 *
 * When a never-spam managed rule kept a spam/suspect verdict out of Junk,
 * Email/llmInspect's spam.deliveryOverride carries "filter:<rule name or
 * id>". The modal shows a line naming both the classifier's verdict and
 * the filter that overrode it, with the "filter:" wire prefix stripped.
 * The line is absent when no override applied.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, cleanup } from '@testing-library/svelte';
import { i18n } from '../i18n/i18n.svelte';
import type { MessageLLMInspect } from './transparency.svelte';

// i18n is NOT mocked here (matches store.setError.test.ts) -- the point
// of this test is that the real "{verdict}" / "{filterName}" placeholders
// in the real EN dictionary interpolate correctly.

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

describe('LLMInspectModal delivery-override line (issue #382)', () => {
  beforeEach(() => {
    i18n.locale = 'en';
  });

  afterEach(() => {
    cleanup();
  });

  it('shows the override line with the verdict and the rule name, prefix stripped', async () => {
    fetchInspectResult = {
      emailId: 'email-1',
      spam: {
        verdict: 'spam',
        confidence: 0.92,
        reason: 'Matches known spam pattern',
        promptApplied: 'Classify the following email...',
        model: 'llama3.2',
        classifiedAt: '2026-09-01T12:00:00Z',
        deliveryOverride: 'filter:Trusted senders',
      },
    };

    render(LLMInspectModal, { props: { emailId: 'email-1', onClose: () => {} } });

    expect(
      await screen.findByText('Classifier said spam, delivered by your filter "Trusted senders".'),
    ).toBeInTheDocument();
  });

  it('falls back to the raw value when the override has no "filter:" prefix', async () => {
    fetchInspectResult = {
      emailId: 'email-2',
      spam: {
        verdict: 'suspect',
        confidence: 0.6,
        reason: 'Borderline',
        promptApplied: 'Classify the following email...',
        model: 'llama3.2',
        classifiedAt: '2026-09-01T12:00:00Z',
        deliveryOverride: '42',
      },
    };

    render(LLMInspectModal, { props: { emailId: 'email-2', onClose: () => {} } });

    expect(
      await screen.findByText('Classifier said suspect, delivered by your filter "42".'),
    ).toBeInTheDocument();
  });

  it('shows no override line when spam.deliveryOverride is absent', async () => {
    fetchInspectResult = {
      emailId: 'email-3',
      spam: {
        verdict: 'spam',
        confidence: 0.97,
        reason: 'Matches known spam pattern',
        promptApplied: 'Classify the following email...',
        model: 'llama3.2',
        classifiedAt: '2026-09-01T12:00:00Z',
      },
    };

    render(LLMInspectModal, { props: { emailId: 'email-3', onClose: () => {} } });

    expect(await screen.findByText('spam')).toBeInTheDocument();
    expect(screen.queryByText(/delivered by your filter/)).not.toBeInTheDocument();
  });
});
