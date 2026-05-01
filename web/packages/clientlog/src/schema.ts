/**
 * Event schema types for the client-log pipeline.
 *
 * Full schema: REQ-OPS-202 (authenticated endpoint).
 * Narrow schema: REQ-OPS-207 (anonymous endpoint -- strict subset).
 *
 * These types are used at flush time to construct the JSON payload.
 * Internal captured events store the minimum needed; enrichment is
 * added at flush time.
 */

export type EventKind = 'error' | 'log' | 'vital';
export type EventLevel = 'trace' | 'debug' | 'info' | 'warn' | 'error';
export type AppName = 'suite' | 'admin';

export type Breadcrumb =
  | { kind: 'route'; ts: string; route: string }
  | { kind: 'fetch'; ts: string; method: string; url_path: string; status?: number }
  | { kind: 'console'; ts: string; level: 'warn' | 'error'; msg: string };

export interface VitalPayload {
  name: 'LCP' | 'INP' | 'CLS' | 'FCP' | 'TTFB';
  value: number;
  id: string;
}

/**
 * Full event schema (authenticated endpoint, REQ-OPS-202).
 */
export interface FullEvent {
  v: 1;
  kind: EventKind;
  level: EventLevel;
  msg: string;
  stack?: string;
  client_ts: string;
  seq: number;
  page_id: string;
  session_id: string;
  app: AppName;
  build_sha: string;
  route: string;
  request_id?: string;
  ua: string;
  breadcrumbs?: Breadcrumb[];
  vital?: VitalPayload;
  synchronous?: boolean;
}

/**
 * Narrow event schema (anonymous endpoint, REQ-OPS-207).
 * Strict subset: no breadcrumbs, no request_id, no session_id, no vital.
 */
export interface NarrowEvent {
  v: 1;
  kind: EventKind;
  level: EventLevel;
  msg: string;
  stack?: string;
  client_ts: string;
  seq: number;
  page_id: string;
  app: AppName;
  build_sha: string;
  route: string;
  ua: string;
}

export type WireEvent = FullEvent | NarrowEvent;

/**
 * Shape of the HTTP request body sent to either endpoint (REQ-OPS-201).
 */
export interface BatchBody {
  events: WireEvent[];
}

/**
 * An event as captured internally (pre-flush enrichment).
 * Stores the minimum fields plus the breadcrumb snapshot (for errors).
 */
export interface CapturedEvent {
  kind: EventKind;
  level: EventLevel;
  msg: string;
  stack?: string;
  client_ts: string;
  seq: number;
  request_id?: string;
  breadcrumbs_snapshot?: Breadcrumb[];
  vital?: VitalPayload;
  synchronous?: boolean;
}
