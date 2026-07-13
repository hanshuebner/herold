/**
 * Mailing lists (roster hosts) list state class (epic #183, REQ-MLIST-42).
 *
 * Loads GET /api/v1/lists (returns the {items, next} pageDTO envelope --
 * internal/protoadmin/mlist.go's mailingListPage). MailingListSummary
 * mirrors internal/protoadmin/mlist_dto.go's mailingListDTO field-for-field:
 * every id is a JSON number, subject_tag/max_message_size_bytes are
 * omitempty (absent rather than null/empty when unset), and posting_policy /
 * subscribe_policy are read-only in S1 (server always sends "open" /
 * "closed" -- see docs/design/server/requirements/28-mailing-lists.md).
 */

import { apiGet, apiPost } from '../api/client';
import { t } from '../i18n/i18n.svelte';

export interface MailingListSummary {
  id: number;
  principal_id: number;
  posting_address: string;
  domain: string;
  display_name: string;
  owner_principal_id: number;
  subject_tag?: string;
  arc_seal: boolean;
  /**
   * True when arc_seal is enabled but domain currently has no active DKIM
   * key (REQ-MLIST-21, issue #183): fanned-out copies go out unsealed.
   * Computed by the server at read time; absent (falsy) when arc_seal is
   * false or the domain has an active key.
   */
  dkim_key_missing?: boolean;
  posting_policy: string;
  subscribe_policy: string;
  max_message_size_bytes?: number;
  created_at: string;
  updated_at: string;
}

/** Wire shape for POST /api/v1/lists (createMailingListRequest). */
export interface CreateMailingListPayload {
  posting_address: string;
  display_name: string;
  owner_principal_id?: number;
  subject_tag?: string;
  arc_seal?: boolean;
  max_message_size_bytes?: number;
}

export type MailingListsStatus = 'idle' | 'loading' | 'ready' | 'error';

export interface OpResult {
  ok: boolean;
  errorMessage: string | null;
}

const PAGE_LIMIT = 100;

class MailingListsState {
  status = $state<MailingListsStatus>('idle');
  items = $state<MailingListSummary[]>([]);
  cursor = $state<string | null>(null);
  hasMore = $state(false);
  errorMessage = $state<string | null>(null);

  async load(): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;
    this.cursor = null;
    this.items = [];
    this.hasMore = false;

    const result = await apiGet<{ items: MailingListSummary[]; next: string | null }>(
      `/api/v1/lists?limit=${PAGE_LIMIT}`,
    );

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? t('mlists.error.loadFailed');
      this.status = 'error';
      return;
    }

    this.items = result.data.items ?? [];
    this.cursor = result.data.next ?? null;
    this.hasMore = this.cursor !== null;
    this.status = 'ready';
  }

  async loadMore(): Promise<void> {
    if (!this.hasMore || this.status === 'loading' || this.cursor === null) return;
    this.status = 'loading';

    const result = await apiGet<{ items: MailingListSummary[]; next: string | null }>(
      `/api/v1/lists?after=${this.cursor}&limit=${PAGE_LIMIT}`,
    );

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? t('mlists.error.loadMoreFailed');
      this.status = 'ready';
      return;
    }

    this.items = [...this.items, ...(result.data.items ?? [])];
    this.cursor = result.data.next ?? null;
    this.hasMore = this.cursor !== null;
    this.status = 'ready';
  }

  async refresh(): Promise<void> {
    await this.load();
  }

  async create(payload: CreateMailingListPayload): Promise<OpResult & { id?: number }> {
    const result = await apiPost<MailingListSummary>('/api/v1/lists', payload);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('mlists.error.createFailed') };
    }
    await this.load();
    return { ok: true, errorMessage: null, id: result.data?.id };
  }
}

export const mlists = new MailingListsState();
