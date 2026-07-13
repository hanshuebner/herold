/**
 * Mailing list detail + roster state class (epic #183, REQ-MLIST-42).
 *
 * Composes:
 *   - GET /api/v1/lists/{id}                    -> mailingListDTO
 *   - GET /api/v1/lists/{id}/members             -> {items, next} pageDTO of
 *     mailingListMemberDTO, filterable by ?state= and ?delivery_mode=
 *   - PATCH /api/v1/lists/{id}                   -> config edits
 *   - DELETE /api/v1/lists/{id}
 *   - POST/PATCH/DELETE .../members[/{mid}]      -> roster edits
 *   - POST .../members/import, GET .../members/export -> bulk ops
 *
 * MailingListMember mirrors internal/protoadmin/mlist_dto.go's
 * mailingListMemberDTO field-for-field: exactly one of principal_id /
 * external_address is present (the store's XOR constraint, REQ-MLIST-02),
 * state and delivery_mode are strings (not enums on the wire), and
 * bounce_score / last_bounce_at are omitempty.
 */

import { apiGet, apiPost, apiPatch, apiDelete } from '../api/client';
import { t } from '../i18n/i18n.svelte';
import type { MailingListSummary } from './mlists.svelte';

export interface MailingListMember {
  id: number;
  list_id: number;
  principal_id?: number;
  external_address?: string;
  state: string;
  delivery_mode: string;
  bounce_score?: number;
  last_bounce_at?: string;
  added_at: string;
  added_by?: number;
}

/** Wire shape for PATCH /api/v1/lists/{id} (patchMailingListRequest). */
export interface PatchMailingListPayload {
  posting_address?: string;
  display_name?: string;
  owner_principal_id?: number;
  subject_tag?: string;
  arc_seal?: boolean;
  max_message_size_bytes?: number;
  /** Stage 3 self-subscription policy (epic #185, REQ-MLIST-60). */
  subscribe_policy?: string;
}

/** Wire shape for POST /api/v1/lists/{id}/members (createMemberRequest). */
export interface CreateMemberPayload {
  principal_id?: number;
  external_address?: string;
  state?: string;
  delivery_mode?: string;
}

/** Wire shape for PATCH /api/v1/lists/{id}/members/{mid} (patchMemberRequest). */
export interface PatchMemberPayload {
  state?: string;
  delivery_mode?: string;
}

export interface ImportResultItem {
  index: number;
  status: string;
  id?: number;
  detail?: string;
}

export interface ImportMembersResult {
  added: number;
  skipped: number;
  results: ImportResultItem[];
}

/** '' means "no filter" -- matches the server's mlistStateFromString("") case. */
export type MailingListMemberStateFilter =
  | ''
  | 'active'
  | 'suspended'
  | 'unsubscribed'
  | 'pending'
  | 'awaiting-approval';
export type MailingListMemberModeFilter = '' | 'each' | 'nomail';

export type MlistDetailStatus = 'idle' | 'loading' | 'ready' | 'error';
export type MlistMembersStatus = 'idle' | 'loading' | 'ready' | 'error';

export interface OpResult {
  ok: boolean;
  errorMessage: string | null;
}

const MEMBER_PAGE_LIMIT = 100;

class MailingListDetailState {
  status = $state<MlistDetailStatus>('idle');
  list = $state<MailingListSummary | null>(null);
  errorMessage = $state<string | null>(null);

  members = $state<MailingListMember[]>([]);
  membersStatus = $state<MlistMembersStatus>('idle');
  membersErrorMessage = $state<string | null>(null);
  membersCursor = $state<string | null>(null);
  membersHasMore = $state(false);
  stateFilter = $state<MailingListMemberStateFilter>('');
  deliveryModeFilter = $state<MailingListMemberModeFilter>('');

  async load(id: string): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;
    this.list = null;

    const result = await apiGet<MailingListSummary>(`/api/v1/lists/${id}`);
    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? t('mlistDetail.error.loadFailed');
      this.status = 'error';
      return;
    }
    this.list = result.data;
    this.status = 'ready';
    await this.loadMembers(id);
  }

  private membersQuery(id: string, after?: string | null): string {
    const params = new URLSearchParams();
    if (this.stateFilter) params.set('state', this.stateFilter);
    if (this.deliveryModeFilter) params.set('delivery_mode', this.deliveryModeFilter);
    params.set('limit', String(MEMBER_PAGE_LIMIT));
    if (after) params.set('after', after);
    return `/api/v1/lists/${id}/members?${params.toString()}`;
  }

  async loadMembers(id: string): Promise<void> {
    this.membersStatus = 'loading';
    this.membersErrorMessage = null;
    this.members = [];
    this.membersCursor = null;
    this.membersHasMore = false;

    const result = await apiGet<{ items: MailingListMember[]; next: string | null }>(this.membersQuery(id));
    if (!result.ok || result.data === null) {
      this.membersErrorMessage = result.errorMessage ?? t('mlistDetail.error.loadMembersFailed');
      this.membersStatus = 'error';
      return;
    }
    this.members = result.data.items ?? [];
    this.membersCursor = result.data.next ?? null;
    this.membersHasMore = this.membersCursor !== null;
    this.membersStatus = 'ready';
  }

  async loadMoreMembers(id: string): Promise<void> {
    if (!this.membersHasMore || this.membersStatus === 'loading' || this.membersCursor === null) return;
    this.membersStatus = 'loading';

    const result = await apiGet<{ items: MailingListMember[]; next: string | null }>(
      this.membersQuery(id, this.membersCursor),
    );
    if (!result.ok || result.data === null) {
      this.membersErrorMessage = result.errorMessage ?? t('mlistDetail.error.loadMembersFailed');
      this.membersStatus = 'ready';
      return;
    }
    this.members = [...this.members, ...(result.data.items ?? [])];
    this.membersCursor = result.data.next ?? null;
    this.membersHasMore = this.membersCursor !== null;
    this.membersStatus = 'ready';
  }

  async updateList(id: string, payload: PatchMailingListPayload): Promise<OpResult> {
    const result = await apiPatch<MailingListSummary>(`/api/v1/lists/${id}`, payload);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('mlistDetail.error.updateFailed') };
    }
    this.list = result.data;
    return { ok: true, errorMessage: null };
  }

  async deleteList(id: string): Promise<OpResult> {
    const result = await apiDelete<unknown>(`/api/v1/lists/${id}`);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('mlistDetail.error.deleteFailed') };
    }
    return { ok: true, errorMessage: null };
  }

  async addMember(id: string, payload: CreateMemberPayload): Promise<OpResult> {
    const result = await apiPost<MailingListMember>(`/api/v1/lists/${id}/members`, payload);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('mlistDetail.error.addMemberFailed') };
    }
    await this.loadMembers(id);
    return { ok: true, errorMessage: null };
  }

  async updateMember(id: string, memberId: number, payload: PatchMemberPayload): Promise<OpResult> {
    const result = await apiPatch<MailingListMember>(`/api/v1/lists/${id}/members/${memberId}`, payload);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('mlistDetail.error.updateMemberFailed') };
    }
    if (result.data) {
      const updated = result.data;
      this.members = this.members.map((m) => (m.id === memberId ? updated : m));
    }
    return { ok: true, errorMessage: null };
  }

  async removeMember(id: string, memberId: number): Promise<OpResult> {
    const result = await apiDelete<unknown>(`/api/v1/lists/${id}/members/${memberId}`);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('mlistDetail.error.removeMemberFailed') };
    }
    this.members = this.members.filter((m) => m.id !== memberId);
    return { ok: true, errorMessage: null };
  }

  async importMembers(
    id: string,
    members: CreateMemberPayload[],
  ): Promise<OpResult & { result?: ImportMembersResult }> {
    const result = await apiPost<ImportMembersResult>(`/api/v1/lists/${id}/members/import`, { members });
    if (!result.ok || result.data === null) {
      return { ok: false, errorMessage: result.errorMessage ?? t('mlistDetail.error.importFailed') };
    }
    await this.loadMembers(id);
    return { ok: true, errorMessage: null, result: result.data };
  }

  async exportMembers(
    id: string,
  ): Promise<OpResult & { items?: MailingListMember[]; truncated?: boolean }> {
    const result = await apiGet<{ items: MailingListMember[]; next: string | null }>(
      `/api/v1/lists/${id}/members/export?limit=50000`,
    );
    if (!result.ok || result.data === null) {
      return { ok: false, errorMessage: result.errorMessage ?? t('mlistDetail.error.exportFailed') };
    }
    return { ok: true, errorMessage: null, items: result.data.items ?? [], truncated: result.data.next !== null };
  }
}

export const mlistDetail = new MailingListDetailState();
