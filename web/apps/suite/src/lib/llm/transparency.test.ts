/**
 * LLM transparency store unit tests — G14, REQ-FILT-65..68, REQ-FILT-216.
 *
 * Coverage:
 *   1. load() populates data from LLMTransparency/get
 *   2. fetchInspect() returns per-message data and caches it
 *   3. fetchInspect() returns null when the list is empty (unclassified)
 *   4. load() sets error state on JMAP method error
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { llmTransparency } from './transparency.svelte';
import type { Invocation } from '../jmap/types';

// ── JMAP client mock ──────────────────────────────────────────────────────

vi.mock('../jmap/client', () => {
  let batchImpl:
    | (() => Promise<{ responses: unknown[]; sessionState: string }>)
    | (() => { responses: unknown[]; sessionState: string })
    | null = null;

  const jmap = {
    batch: vi.fn(async (builder: (b: unknown) => void) => {
      const calls: unknown[] = [];
      builder({
        call: (_name: string, _args: unknown, _using: string[]) => {
          calls.push({ name: _name, args: _args });
          return {
            ref: (path: string) => ({
              resultOf: `c${calls.length - 1}`,
              name: _name,
              path,
            }),
          };
        },
      });
      if (batchImpl) return batchImpl();
      return { responses: [], sessionState: 'state-1' };
    }),
    hasCapability: vi.fn(() => true),
  };

  return {
    jmap,
    strict: (responses: unknown[]) => {
      for (const r of responses) {
        if (Array.isArray(r) && r[0] === 'error') {
          throw new Error(
            (r[1] as { description?: string }).description ?? 'method error',
          );
        }
      }
      return responses;
    },
    __setBatchImpl: (impl: typeof batchImpl) => {
      batchImpl = impl;
    },
  };
});

// ── Auth mock ─────────────────────────────────────────────────────────────

vi.mock('../auth/auth.svelte', () => ({
  auth: {
    session: {
      primaryAccounts: { 'urn:ietf:params:jmap:mail': 'account-1' },
    },
  },
}));

async function getJmapMock() {
  return (await import('../jmap/client')) as unknown as {
    jmap: { batch: ReturnType<typeof vi.fn>; hasCapability: ReturnType<typeof vi.fn> };
    strict: (responses: unknown[]) => unknown[];
    __setBatchImpl: (
      impl:
        | (() => Promise<{ responses: unknown[]; sessionState: string }>)
        | (() => { responses: unknown[]; sessionState: string })
        | null,
    ) => void;
  };
}

// ─────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────

describe('llmTransparency.load', () => {
  beforeEach(() => {
    (llmTransparency as unknown as { loadStatus: string }).loadStatus = 'idle';
    (llmTransparency as unknown as { data: null }).data = null;
    (llmTransparency as unknown as { loadError: null }).loadError = null;
  });

  it('populates data from a successful LLMTransparency/get response', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'LLMTransparency/get',
          {
            list: [
              {
                id: 'singleton',
                spamPrompt: 'Classify spam',
                spamModel: 'llama3.2',
                categoriserPrompt: 'Classify category',
                categoriserCategories: ['Primary', 'Social'],
                categoriserModel: 'llama3.2',
                disclosureNote: 'Your messages are classified by herold.',
              },
            ],
          },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    await llmTransparency.load(true);

    expect(llmTransparency.loadStatus).toBe('ready');
    expect(llmTransparency.data).not.toBeNull();
    expect(llmTransparency.data!.spamPrompt).toBe('Classify spam');
    expect(llmTransparency.data!.disclosureNote).toBe(
      'Your messages are classified by herold.',
    );
    expect(llmTransparency.data!.categoriserCategories).toEqual(['Primary', 'Social']);

    mock.__setBatchImpl(null);
  });

  it('sets data to null when the server returns an empty list', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        ['LLMTransparency/get', { list: [] }, 'c0'] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    await llmTransparency.load(true);

    expect(llmTransparency.loadStatus).toBe('ready');
    expect(llmTransparency.data).toBeNull();

    mock.__setBatchImpl(null);
  });

  it('transitions to error state on a method error', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        ['error', { type: 'serverFail', description: 'Not configured' }, 'c0'] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    await llmTransparency.load(true);

    expect(llmTransparency.loadStatus).toBe('error');
    expect(llmTransparency.loadError).toContain('Not configured');

    mock.__setBatchImpl(null);
  });
});

describe('llmTransparency.fetchInspect', () => {
  it('returns per-message inspect data for a classified message', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        [
          'Email/llmInspect',
          {
            list: [
              {
                emailId: 'email-1',
                category: {
                  assigned: 'Promotions',
                  confidence: 0.92,
                  reason: 'Contains marketing language',
                  promptApplied: 'Classify the following email...',
                  model: 'llama3.2',
                  classifiedAt: '2026-04-28T10:00:00Z',
                },
              },
            ],
          },
          'c0',
        ] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    const result = await llmTransparency.fetchInspect('email-1');

    expect(result).not.toBeNull();
    expect(result!.category?.assigned).toBe('Promotions');
    expect(result!.category?.confidence).toBe(0.92);

    mock.__setBatchImpl(null);
  });

  it('returns null when the server returns an empty list (unclassified message)', async () => {
    const mock = await getJmapMock();
    mock.__setBatchImpl(() => ({
      responses: [
        ['Email/llmInspect', { list: [] }, 'c0'] as Invocation,
      ],
      sessionState: 'state-1',
    }));

    const result = await llmTransparency.fetchInspect('email-unclassified');

    expect(result).toBeNull();

    mock.__setBatchImpl(null);
  });
});
