/**
 * Message-research state (REQ-ADM-306).
 *
 * Loads GET /api/v1/admin/message-research with before_us cursor + limit.
 * Supports sender, recipient, message_id, subject, date_from, date_to filters.
 * Default limit 50; "Weitere laden" appends via before_us cursor.
 *
 * Results are returned newest-first by the server.
 * Timestamps (at) are RFC3339 strings.
 */

import { apiGet } from '../api/client';

// --- Type definitions -------------------------------------------------------

export interface ReceivedHit {
  source: 'received';
  at: string;
  principal_id: number;
  mailbox_name: string;
  is_junk: boolean;
  spam_verdict?: string;
  spam_confidence?: number;
  envelope: {
    subject: string;
    from: string;
    to: string;
    cc: string;
    bcc: string;
    reply_to: string;
    message_id: string;
    in_reply_to: string;
    references: string;
    date?: string;
  };
}

export interface SmtpEventHit {
  source: 'smtp_event';
  at: string;
  action: string;
  actor_id: string;
  subject: string;
  remote_addr: string;
  outcome: 'success' | 'failure' | 'unknown';
  message: string;
  domain: string;
}

export interface SendOutcomeHit {
  source: 'send_outcome';
  at: string;
  queue_id: number;
  mail_from: string;
  rcpt_to: string;
  envelope_id: string;
  state: string;
  attempts: number;
  last_error?: string;
  last_attempt_at?: string;
}

export type MessageResearchHit = ReceivedHit | SmtpEventHit | SendOutcomeHit;

export type MessageResearchStatus = 'idle' | 'loading' | 'ready' | 'error';

// --- Helpers ----------------------------------------------------------------

/**
 * Convert a datetime-local input value ("YYYY-MM-DDTHH:MM") to RFC3339.
 * The input is treated as UTC. Returns null if the input does not parse.
 *
 * Exported for unit testing.
 */
export function toRFC3339(raw: string): string | null {
  const d = new Date(raw.includes(':') ? raw + ':00Z' : raw + 'T00:00:00Z');
  if (isNaN(d.getTime())) return null;
  return d.toISOString().replace('.000Z', 'Z');
}

// --- State class ------------------------------------------------------------

const PAGE_LIMIT = 50;

class MessageResearchState {
  status = $state<MessageResearchStatus>('idle');
  items = $state<MessageResearchHit[]>([]);
  cursor = $state<string | null>(null);
  hasMore = $state(false);
  errorMessage = $state<string | null>(null);

  senderFilter = $state('');
  recipientFilter = $state('');
  messageIdFilter = $state('');
  subjectFilter = $state('');
  dateFromFilter = $state('');
  dateToFilter = $state('');

  buildUrl(beforeUs?: string): string {
    const params = new URLSearchParams();
    params.set('limit', String(PAGE_LIMIT));
    if (this.senderFilter.trim()) {
      params.set('sender', this.senderFilter.trim());
    }
    if (this.recipientFilter.trim()) {
      params.set('recipient', this.recipientFilter.trim());
    }
    if (this.messageIdFilter.trim()) {
      params.set('message_id', this.messageIdFilter.trim());
    }
    if (this.subjectFilter.trim()) {
      params.set('subject', this.subjectFilter.trim());
    }
    if (this.dateFromFilter.trim()) {
      const dateFrom = toRFC3339(this.dateFromFilter.trim());
      if (dateFrom) params.set('date_from', dateFrom);
    }
    if (this.dateToFilter.trim()) {
      const dateTo = toRFC3339(this.dateToFilter.trim());
      if (dateTo) params.set('date_to', dateTo);
    }
    if (beforeUs) {
      params.set('before_us', beforeUs);
    }
    return `/api/v1/admin/message-research?${params.toString()}`;
  }

  async load(): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;
    this.cursor = null;
    this.items = [];
    this.hasMore = false;

    const result = await apiGet<{ items: MessageResearchHit[]; next: string | null }>(
      this.buildUrl(),
    );

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? 'Nachrichten konnten nicht geladen werden';
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

    const result = await apiGet<{ items: MessageResearchHit[]; next: string | null }>(
      this.buildUrl(this.cursor),
    );

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? 'Weitere Nachrichten konnten nicht geladen werden';
      this.status = 'ready';
      return;
    }

    this.items = [...this.items, ...(result.data.items ?? [])];
    this.cursor = result.data.next ?? null;
    this.hasMore = this.cursor !== null;
    this.status = 'ready';
  }

  resetFilters(): void {
    this.senderFilter = '';
    this.recipientFilter = '';
    this.messageIdFilter = '';
    this.subjectFilter = '';
    this.dateFromFilter = '';
    this.dateToFilter = '';
  }
}

export const messageResearch = new MessageResearchState();
