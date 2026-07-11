/**
 * Queue item detail state class.
 *
 * Fetches a single queue item from GET /api/v1/queue/{id} and exposes
 * action methods (retry, hold, release, delete) as one-shot calls.
 */

import { apiGet, apiPost, apiDelete } from '../api/client';
import type { QueueItem } from './queue.svelte';
import { t } from '../i18n/i18n.svelte';

export type QueueItemStatus = 'idle' | 'loading' | 'ready' | 'error';

export interface OpResult {
  ok: boolean;
  errorMessage: string | null;
}

class QueueItemState {
  status = $state<QueueItemStatus>('idle');
  item = $state<QueueItem | null>(null);
  errorMessage = $state<string | null>(null);

  async load(id: string): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;
    this.item = null;

    const result = await apiGet<QueueItem>(`/api/v1/queue/${id}`);

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? t('queueItem.error.loadFailed');
      this.status = 'error';
      return;
    }

    this.item = result.data;
    this.status = 'ready';
  }

  async retry(id: string): Promise<OpResult> {
    const result = await apiPost<unknown>(`/api/v1/queue/${id}/retry`);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('queueItem.error.retryFailed') };
    }
    await this.load(id);
    return { ok: true, errorMessage: null };
  }

  async hold(id: string): Promise<OpResult> {
    const result = await apiPost<unknown>(`/api/v1/queue/${id}/hold`);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('queueItem.error.holdFailed') };
    }
    await this.load(id);
    return { ok: true, errorMessage: null };
  }

  async release(id: string): Promise<OpResult> {
    const result = await apiPost<unknown>(`/api/v1/queue/${id}/release`);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('queueItem.error.releaseFailed') };
    }
    await this.load(id);
    return { ok: true, errorMessage: null };
  }

  async deleteItem(id: string): Promise<OpResult> {
    const result = await apiDelete<unknown>(`/api/v1/queue/${id}`);
    if (!result.ok) {
      return { ok: false, errorMessage: result.errorMessage ?? t('queueItem.error.deleteFailed') };
    }
    return { ok: true, errorMessage: null };
  }
}

export const queueItem = new QueueItemState();
