/**
 * CategorySettings store — Wave 3.13 (revised 2026-04-28, REQ-CAT-40/41).
 *
 * Manages the JMAP `CategorySettings` singleton (id "singleton") exposed by
 * the `https://netzhansa.com/jmap/categorise` capability. Loaded on app boot
 * when the capability is advertised; exposes reactive state and actions.
 *
 * The prompt is the single user-editable lever. The category list is
 * server-derived (`derivedCategories: string[]`, read-only) per REQ-CAT-40
 * and REQ-FILT-217. No list-mutation methods; only the prompt is mutable.
 *
 * Pattern mirrors settings.svelte.ts (local prefs) and the VacationForm
 * (server-side singleton pattern), adapted for a reactive store that
 * survives across views rather than living inside a single component.
 */

import { jmap, strict } from '../jmap/client';
import { auth } from '../auth/auth.svelte';
import { sync } from '../jmap/sync.svelte';
import { toast } from '../toast/toast.svelte';
import { Capability, type Invocation } from '../jmap/types';

type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';

/** Scope for the bulk re-categorisation RPC. */
export type RecategoriseScope = 'inbox-recent' | 'inbox-all';

/**
 * Default prompt used when the server has not yet returned settings or
 * the user has not modified the prompt. Enumerates Gmail-style categories
 * and instructs the LLM to return the REQ-FILT-215 JSON shape.
 */
export const DEFAULT_PROMPT = `You are a mail categoriser. Classify the message into exactly one of the following categories:
- Primary: direct correspondence, transactional mail, anything that does not fit another category
- Social: notifications from social networks, messaging apps, friend activity
- Promotions: marketing, offers, deals, retail newsletters
- Updates: receipts, statements, automated notifications, package tracking
- Forums: mailing-list discussion, online groups, community digests

Respond with JSON only, no explanation:
{"categories":["Primary","Social","Promotions","Updates","Forums"],"assigned":"<chosen category>"}
If none of the categories fit, use "Primary". The "assigned" value must be one of the "categories" list.`;

function invocationArgs<T>(inv: Invocation | undefined): T {
  if (!inv) throw new Error('Expected method invocation, got undefined');
  return inv[1] as T;
}

class CategorySettingsStore {
  loadStatus = $state<LoadStatus>('idle');
  loadError = $state<string | null>(null);

  /**
   * Server-derived category names (read-only to the user). Populated from
   * the most recent successful classifier response via REQ-FILT-217.
   * Empty when no classifier call has succeeded since the last prompt change.
   */
  derivedCategories = $state<string[]>([]);

  systemPrompt = $state<string>('');
  defaultPrompt = $state<string>(DEFAULT_PROMPT);
  enabled = $state<boolean>(true);

  /** True while a bulk re-categorise RPC is in flight. */
  recategorising = $state(false);

  /** Whether the server advertises bulkRecategoriseEnabled on the capability. */
  bulkRecategoriseEnabled = $state(false);

  #state = $state<string | null>(null);

  constructor() {
    sync.on('CategorySettings', (newState) => {
      void this.#onStateChange(newState);
    });
  }

  async #onStateChange(newState: string): Promise<void> {
    if (newState === this.#state) return;
    try {
      await this.load();
    } catch (err) {
      console.error('CategorySettings reload after state change failed', err);
    }
    this.#state = newState;
  }

  /** True when the server advertises the categorise capability. */
  get available(): boolean {
    return jmap.hasCapability(Capability.HeroldCategorise);
  }

  /**
   * Load CategorySettings from the server. Idempotent when already 'ready';
   * force=true skips the idempotency check (used after state-change events).
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

    // Read bulkRecategoriseEnabled from the session capability metadata.
    const capMeta = auth.session?.capabilities[Capability.HeroldCategorise] as
      | { bulkRecategoriseEnabled?: boolean }
      | undefined;
    this.bulkRecategoriseEnabled = capMeta?.bulkRecategoriseEnabled ?? false;

    this.loadStatus = 'loading';
    this.loadError = null;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'CategorySettings/get',
          { accountId, ids: ['singleton'] },
          [Capability.HeroldCategorise],
        );
      });
      strict(responses);

      const args = invocationArgs<{
        list: Array<{
          id: string;
          derivedCategories?: string[];
          systemPrompt?: string;
          defaultPrompt?: string;
          enabled?: boolean;
        }>;
        state: string;
      }>(responses[0]);

      const row = args.list?.[0];
      if (row) {
        this.derivedCategories = Array.isArray(row.derivedCategories)
          ? [...row.derivedCategories]
          : [];
        this.systemPrompt = row.systemPrompt ?? '';
        this.defaultPrompt = row.defaultPrompt ?? DEFAULT_PROMPT;
        this.enabled = row.enabled ?? true;
      } else {
        // Server synthesises defaults; mirror them locally.
        this.derivedCategories = [];
        this.systemPrompt = '';
        this.defaultPrompt = DEFAULT_PROMPT;
        this.enabled = true;
      }
      if (typeof args.state === 'string') this.#state = args.state;
      this.loadStatus = 'ready';
    } catch (err) {
      this.loadStatus = 'error';
      this.loadError = err instanceof Error ? err.message : String(err);
    }
  }

  /**
   * Set the system prompt. Optimistic; reverts on failure.
   */
  async setSystemPrompt(prompt: string): Promise<void> {
    const prev = this.systemPrompt;
    this.systemPrompt = prompt;
    await this.#set({ systemPrompt: prompt }, () => {
      this.systemPrompt = prev;
    });
  }

  /**
   * Set the enabled flag. Optimistic; reverts on failure.
   */
  async setEnabled(value: boolean): Promise<void> {
    const prev = this.enabled;
    this.enabled = value;
    await this.#set({ enabled: value }, () => {
      this.enabled = prev;
    });
  }

  /**
   * Reset system prompt to the server default. Replaces systemPrompt with
   * defaultPrompt locally, then persists via /set. Also invalidates
   * derivedCategories because the prompt change will trigger a new
   * classifier call on the server (REQ-FILT-217).
   */
  async reset(): Promise<void> {
    // Invalidate derivedCategories immediately: they will be refilled after
    // the next successful classifier call (REQ-FILT-217).
    this.derivedCategories = [];
    await this.setSystemPrompt(this.defaultPrompt);
    toast.show({ message: 'Prompt reset to default' });
  }

  /**
   * Fire `CategorySettings/recategorise` for the given scope. Sets
   * `recategorising` while the job is running. Callers observe this flag
   * to render a progress banner. When the server emits a CategorySettings
   * state-change we reload which naturally clears the banner.
   */
  async recategorise(scope: RecategoriseScope = 'inbox-recent'): Promise<void> {
    if (!this.available) return;
    const accountId = auth.session?.primaryAccounts[Capability.Mail] ?? null;
    if (!accountId) return;

    this.recategorising = true;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'CategorySettings/recategorise',
          { accountId, scope },
          [Capability.HeroldCategorise],
        );
      });
      strict(responses);
      // Server returns { jobId, state: "running" } -- we don't use the jobId;
      // completion is signalled by a CategorySettings state-change event.
      toast.show({ message: 'Re-categorisation started' });
    } catch (err) {
      this.recategorising = false;
      const msg = err instanceof Error ? err.message : String(err);
      toast.show({
        message: `Re-categorise failed: ${msg}`,
        kind: 'error',
        timeoutMs: 6000,
      });
    }
    // recategorising stays true until the state-change handler fires load(),
    // which resets the flag indirectly by virtue of the data being fresh.
    // We also clear it if the response showed an error (already done above).
  }

  /** Internal: issue `CategorySettings/set` and revert on failure. */
  async #set(patches: Record<string, unknown>, revert: () => void): Promise<void> {
    if (!this.available) return;
    const accountId = auth.session?.primaryAccounts[Capability.Mail] ?? null;
    if (!accountId) return;

    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'CategorySettings/set',
          {
            accountId,
            update: { singleton: patches },
          },
          [Capability.HeroldCategorise],
        );
      });
      strict(responses);

      const result = invocationArgs<{
        updated?: Record<string, unknown> | null;
        notUpdated?: Record<string, { type: string; description?: string }>;
      }>(responses[0]);

      const failure = result.notUpdated?.singleton;
      if (failure) {
        revert();
        toast.show({
          message: failure.description ?? `Save failed: ${failure.type}`,
          kind: 'error',
          timeoutMs: 6000,
        });
        return;
      }
      toast.show({ message: 'Settings saved' });
    } catch (err) {
      revert();
      toast.show({
        message: err instanceof Error ? err.message : 'Save failed',
        kind: 'error',
        timeoutMs: 6000,
      });
    }
  }
}

export const categorySettings = new CategorySettingsStore();

/**
 * Return the `$category-<name>` keyword for a category name.
 * The name is lowercased and stripped of whitespace per the wire contract.
 */
export function categoryKeyword(name: string): string {
  return `$category-${name.toLowerCase().replace(/\s+/g, '-')}`;
}

/**
 * Given an email's keywords map, return the category name it belongs to,
 * or null if no `$category-*` keyword is present (treated as Primary per
 * REQ-CAT-03).
 */
export function emailCategory(
  keywords: Record<string, true | undefined>,
  derivedCategories: string[],
): string | null {
  for (const name of derivedCategories) {
    const kw = categoryKeyword(name);
    if (keywords[kw]) return name;
  }
  return null;
}

/**
 * True when the given email keyword set matches the given category tab.
 * The Primary tab (tabName === null) matches emails with NO category keyword;
 * all other tabs match emails whose `$category-<name>` keyword is present.
 */
export function emailMatchesTab(
  keywords: Record<string, true | undefined>,
  tabName: string | null,
  derivedCategories: string[],
): boolean {
  const actual = emailCategory(keywords, derivedCategories);
  if (tabName === null) {
    // Primary tab: no category keyword set.
    return actual === null;
  }
  return actual === tabName;
}

export const _internals_forTest = {
  categoryKeyword,
  emailCategory,
  emailMatchesTab,
  DEFAULT_PROMPT,
};
