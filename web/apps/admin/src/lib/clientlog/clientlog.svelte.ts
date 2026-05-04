/**
 * Client-log viewer state.
 *
 * Loads GET /api/v1/admin/clientlog (REQ-ADM-230) with cursor pagination
 * and server-side filters. Provides actions for:
 *   - opening the detail pane for a row
 *   - fetching the request timeline (REQ-ADM-231)
 *   - enabling / disabling live-tail (REQ-ADM-232)
 *   - loading clientlog stats (REQ-ADM-233)
 *   - triggering client-side symbolication (REQ-OPS-212)
 *
 * REQ-OPS-218: this module does NOT parse or evaluate field values as HTML.
 * All values from the server are plain text; rendering is the view's
 * responsibility.
 */

import { apiGet, apiPost, apiDelete } from '../api/client';
import { encodeFilters, DEFAULT_FILTERS } from './filters';
import type { ClientlogFilters } from './filters';
import { symbolicateStack, SymbolicateError } from './symbolicate';

// ---------------------------------------------------------------------------
// Wire types (shaped after clientLogRowDTO in clientlog_admin.go)
// ---------------------------------------------------------------------------

export interface Breadcrumb {
  kind: 'route' | 'fetch' | 'console';
  ts: string;
  route?: string;
  method?: string;
  url_path?: string;
  status?: number;
  level?: string;
  msg?: string;
}

export interface VitalData {
  name: string;
  value: number;
  id: string;
}

/** Shape of the raw original event nested inside the enriched payload. */
export interface RawEvent {
  v?: number;
  kind?: string;
  level?: string;
  msg?: string;
  client_ts?: string;
  seq?: number;
  page_id?: string;
  session_id?: string;
  app?: string;
  build_sha?: string;
  route?: string;
  ua?: string;
  breadcrumbs?: Breadcrumb[];
  vital?: VitalData;
  [key: string]: unknown;
}

/**
 * Shape of the enriched payload stored in ring-buffer payload_json.
 * The server wraps the original event in an envelope and stores it as
 * { server_recv_ts, clock_skew_ms, listener, endpoint, raw: <original event> }.
 */
export interface ClientlogPayload {
  server_recv_ts?: string;
  clock_skew_ms?: number;
  user_id?: string;
  listener?: string;
  endpoint?: string;
  raw?: RawEvent;
}

export interface ClientlogRow {
  id: number;
  slice: string;
  server_ts: string;
  client_ts: string;
  clock_skew_ms: number;
  app: string;
  kind: string;
  level: string;
  user_id?: string;
  session_id?: string;
  page_id: string;
  request_id?: string;
  route?: string;
  build_sha: string;
  ua: string;
  msg: string;
  stack?: string;
  payload?: ClientlogPayload;
}

export interface TimelineEntry {
  source: 'client' | 'server';
  effective_ts: string;
  clientlog?: ClientlogRow;
  serverlog?: Record<string, unknown>;
}

export interface ClientlogStats {
  received_total: Record<string, number>;
  dropped_total: Record<string, number>;
  ring_buffer_rows: Record<string, number>;
}

// ---------------------------------------------------------------------------
// State class
// ---------------------------------------------------------------------------

export type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';
export type SymbolicateStatus = 'idle' | 'loading' | 'done' | 'error';

class ClientlogState {
  // List view
  status = $state<LoadStatus>('idle');
  rows = $state<ClientlogRow[]>([]);
  cursor = $state<string | null>(null);
  hasMore = $state(false);
  errorMessage = $state<string | null>(null);

  // Filter state (bind directly from the view)
  filters = $state<ClientlogFilters>({ ...DEFAULT_FILTERS });

  // Detail pane
  selected = $state<ClientlogRow | null>(null);

  // Symbolication (per open detail pane)
  symbolicateStatus = $state<SymbolicateStatus>('idle');
  symbolicateError = $state<string | null>(null);
  symbolicatedStack = $state<string | null>(null);

  // Timeline
  timelineStatus = $state<LoadStatus>('idle');
  timelineEntries = $state<TimelineEntry[]>([]);
  timelineError = $state<string | null>(null);

  // Live-tail
  livetailStatus = $state<'idle' | 'pending' | 'active' | 'error'>('idle');
  livetailUntil = $state<string | null>(null);
  livetailError = $state<string | null>(null);

  // Stats (for the dashboard tile)
  statsStatus = $state<LoadStatus>('idle');
  stats = $state<ClientlogStats | null>(null);
  statsError = $state<string | null>(null);

  // ---------------------------------------------------------------------------
  // List / filter actions
  // ---------------------------------------------------------------------------

  private buildUrl(cursor?: string): string {
    const p = encodeFilters(this.filters, cursor);
    return `/api/v1/admin/clientlog?${p.toString()}`;
  }

  async load(): Promise<void> {
    this.status = 'loading';
    this.errorMessage = null;
    this.cursor = null;
    this.rows = [];
    this.hasMore = false;

    const result = await apiGet<{ rows: ClientlogRow[]; next_cursor?: string }>(
      this.buildUrl(),
    );

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? 'Failed to load client logs';
      this.status = 'error';
      return;
    }

    this.rows = result.data.rows ?? [];
    this.cursor = result.data.next_cursor ?? null;
    this.hasMore = this.cursor !== null;
    this.status = 'ready';
  }

  async loadMore(): Promise<void> {
    if (!this.hasMore || this.status === 'loading' || this.cursor === null) return;
    this.status = 'loading';

    const result = await apiGet<{ rows: ClientlogRow[]; next_cursor?: string }>(
      this.buildUrl(this.cursor),
    );

    if (!result.ok || result.data === null) {
      this.errorMessage = result.errorMessage ?? 'Failed to load more entries';
      this.status = 'ready';
      return;
    }

    this.rows = [...this.rows, ...(result.data.rows ?? [])];
    this.cursor = result.data.next_cursor ?? null;
    this.hasMore = this.cursor !== null;
    this.status = 'ready';
  }

  resetFilters(): void {
    this.filters = { ...DEFAULT_FILTERS };
  }

  // ---------------------------------------------------------------------------
  // Detail pane
  // ---------------------------------------------------------------------------

  openRow(row: ClientlogRow): void {
    this.selected = row;
    // Reset per-pane state when opening a new row.
    this.symbolicateStatus = 'idle';
    this.symbolicateError = null;
    this.symbolicatedStack = null;
    this.timelineStatus = 'idle';
    this.timelineEntries = [];
    this.timelineError = null;
    this.livetailStatus = 'idle';
    this.livetailUntil = null;
    this.livetailError = null;
  }

  closePane(): void {
    this.selected = null;
  }

  // ---------------------------------------------------------------------------
  // Symbolication (REQ-OPS-212)
  // ---------------------------------------------------------------------------

  async symbolicate(): Promise<void> {
    if (this.selected === null || !this.selected.stack) return;
    this.symbolicateStatus = 'loading';
    this.symbolicateError = null;
    this.symbolicatedStack = null;

    try {
      const result = await symbolicateStack(
        this.selected.stack,
        this.selected.build_sha,
      );
      this.symbolicatedStack = result;
      this.symbolicateStatus = 'done';
    } catch (err) {
      if (err instanceof SymbolicateError) {
        this.symbolicateError = err.message;
      } else {
        this.symbolicateError = err instanceof Error ? err.message : String(err);
      }
      this.symbolicateStatus = 'error';
    }
  }

  // ---------------------------------------------------------------------------
  // Timeline (REQ-ADM-231)
  // ---------------------------------------------------------------------------

  async loadTimeline(): Promise<void> {
    if (this.selected === null || !this.selected.request_id) return;
    this.timelineStatus = 'loading';
    this.timelineError = null;
    this.timelineEntries = [];

    const result = await apiGet<TimelineEntry[]>(
      `/api/v1/admin/clientlog/timeline?request_id=${encodeURIComponent(this.selected.request_id)}`,
    );

    if (!result.ok || result.data === null) {
      this.timelineError = result.errorMessage ?? 'Failed to load timeline';
      this.timelineStatus = 'error';
      return;
    }

    // Sort by effective_ts ascending (server already sorts, but belt-and-suspenders).
    const entries = [...(result.data ?? [])].sort(
      (a, b) =>
        new Date(a.effective_ts).getTime() - new Date(b.effective_ts).getTime(),
    );
    this.timelineEntries = entries;
    this.timelineStatus = 'ready';
  }

  // ---------------------------------------------------------------------------
  // Live-tail (REQ-ADM-232)
  // ---------------------------------------------------------------------------

  async enableLivetail(durationStr = '15m'): Promise<void> {
    if (this.selected === null || !this.selected.session_id) return;
    this.livetailStatus = 'pending';
    this.livetailError = null;

    const result = await apiPost<{ session_id: string; livetail_until: string }>(
      '/api/v1/admin/clientlog/livetail',
      { session_id: this.selected.session_id, duration: durationStr },
    );

    if (!result.ok || result.data === null) {
      this.livetailError = result.errorMessage ?? 'Failed to enable live-tail';
      this.livetailStatus = 'error';
      return;
    }

    this.livetailUntil = result.data.livetail_until;
    this.livetailStatus = 'active';
  }

  async disableLivetail(): Promise<void> {
    if (this.selected === null || !this.selected.session_id) return;
    this.livetailStatus = 'pending';
    this.livetailError = null;

    const result = await apiDelete<null>(
      `/api/v1/admin/clientlog/livetail/${encodeURIComponent(this.selected.session_id)}`,
    );

    if (!result.ok) {
      this.livetailError = result.errorMessage ?? 'Failed to disable live-tail';
      this.livetailStatus = 'active';
      return;
    }

    this.livetailUntil = null;
    this.livetailStatus = 'idle';
  }

  // ---------------------------------------------------------------------------
  // Stats (REQ-ADM-233)
  // ---------------------------------------------------------------------------

  async loadStats(): Promise<void> {
    this.statsStatus = 'loading';
    this.statsError = null;

    const result = await apiGet<ClientlogStats>('/api/v1/admin/clientlog/stats');

    if (!result.ok || result.data === null) {
      this.statsError = result.errorMessage ?? 'Failed to load stats';
      this.statsStatus = 'error';
      return;
    }

    this.stats = result.data;
    this.statsStatus = 'ready';
  }
}

export const clientlog = new ClientlogState();
