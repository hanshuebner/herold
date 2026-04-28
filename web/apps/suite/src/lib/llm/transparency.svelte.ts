/**
 * LLM transparency store — G14, REQ-FILT-65..68, REQ-FILT-216.
 *
 * Exposes:
 *   - LLMTransparency/get (singleton) for settings-panel disclosure:
 *     spam prompt + categoriser prompt + disclosure note.
 *   - Email/llmInspect for per-message inspect modal.
 *
 * Capability: https://netzhansa.com/jmap/llm-transparency
 */

import { jmap, strict } from '../jmap/client';
import { auth } from '../auth/auth.svelte';
import { Capability } from '../jmap/types';
import type { Invocation } from '../jmap/types';

export interface LLMTransparencyData {
  spamPrompt: string;
  spamModel: string;
  categoriserPrompt: string;
  categoriserCategories: string[];
  categoriserModel: string;
  disclosureNote: string;
}

export interface MessageLLMInspect {
  emailId: string;
  spam?: {
    verdict: string;
    confidence: number;
    reason: string;
    promptApplied: string;
    model: string;
    classifiedAt: string;
  };
  category?: {
    assigned: string;
    confidence: number;
    reason: string;
    promptApplied: string;
    model: string;
    classifiedAt: string;
  };
}

type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';

function invocationArgs<T>(inv: Invocation | undefined): T {
  if (!inv) throw new Error('Expected method invocation, got undefined');
  return inv[1] as T;
}

class LLMTransparencyStore {
  /** Singleton data from LLMTransparency/get. */
  data = $state<LLMTransparencyData | null>(null);
  loadStatus = $state<LoadStatus>('idle');
  loadError = $state<string | null>(null);

  /** Per-message inspect results, keyed by emailId. */
  #inspectCache = $state<Map<string, MessageLLMInspect>>(new Map());

  /** True when the server advertises the llm-transparency capability. */
  get available(): boolean {
    return jmap.hasCapability(Capability.HeroldLLMTransparency);
  }

  /**
   * Load the LLMTransparency singleton. Idempotent when already 'ready';
   * force=true re-fetches.
   */
  async load(force = false): Promise<void> {
    if (!this.available) return;
    if (!force && this.loadStatus === 'ready') return;
    if (this.loadStatus === 'loading') return;

    const accountId = auth.session?.primaryAccounts[Capability.Mail] ?? null;
    if (!accountId) {
      this.loadStatus = 'error';
      this.loadError = 'No Mail account on this session';
      return;
    }

    this.loadStatus = 'loading';
    this.loadError = null;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'LLMTransparency/get',
          { accountId, ids: ['singleton'] },
          [Capability.HeroldLLMTransparency],
        );
      });
      strict(responses);

      const args = invocationArgs<{
        list: Array<LLMTransparencyData & { id: string }>;
      }>(responses[0]);

      const row = args.list?.[0];
      if (row) {
        this.data = {
          spamPrompt: row.spamPrompt ?? '',
          spamModel: row.spamModel ?? '',
          categoriserPrompt: row.categoriserPrompt ?? '',
          categoriserCategories: row.categoriserCategories ?? [],
          categoriserModel: row.categoriserModel ?? '',
          disclosureNote: row.disclosureNote ?? '',
        };
      } else {
        this.data = null;
      }
      this.loadStatus = 'ready';
    } catch (err) {
      this.loadStatus = 'error';
      this.loadError = err instanceof Error ? err.message : String(err);
    }
  }

  /**
   * Fetch the per-message LLM inspect result for an email.
   * Results are cached per email id for the lifetime of the store instance.
   * Returns undefined while loading.
   */
  inspectResult(emailId: string): MessageLLMInspect | 'loading' | 'error' | null {
    const cached = this.#inspectCache.get(emailId);
    if (cached) return cached;
    return null;
  }

  /** Fetch inspect data for the given email. Caches the result. */
  async fetchInspect(emailId: string): Promise<MessageLLMInspect | null> {
    if (!this.available) return null;
    const cached = this.#inspectCache.get(emailId);
    if (cached) return cached;

    const accountId = auth.session?.primaryAccounts[Capability.Mail] ?? null;
    if (!accountId) return null;

    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'Email/llmInspect',
          { accountId, ids: [emailId] },
          [Capability.HeroldLLMTransparency],
        );
      });
      strict(responses);

      const args = invocationArgs<{
        list: Array<MessageLLMInspect>;
      }>(responses[0]);

      const row = args.list?.[0];
      if (row) {
        const next = new Map(this.#inspectCache);
        next.set(emailId, row);
        this.#inspectCache = next;
        return row;
      }
      return null;
    } catch {
      return null;
    }
  }
}

export const llmTransparency = new LLMTransparencyStore();
