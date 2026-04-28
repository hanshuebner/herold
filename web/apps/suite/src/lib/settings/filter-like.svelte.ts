/**
 * filter-like.svelte.ts — cross-component coordination for "Filter messages
 * like this" (REQ-MAIL-138).
 *
 * MessageAccordion sets a pending payload; FiltersForm reads it on mount
 * (via $effect) and opens the editor pre-populated with the derived
 * conditions. The payload is consumed exactly once.
 */

import type { RuleCondition } from './managed-rules.svelte';

interface FilterLikePayload {
  conditions: RuleCondition[];
}

class FilterLikeState {
  pending = $state<FilterLikePayload | null>(null);

  set(payload: FilterLikePayload): void {
    this.pending = payload;
  }

  consume(): FilterLikePayload | null {
    const p = this.pending;
    this.pending = null;
    return p;
  }
}

export const filterLike = new FilterLikeState();
