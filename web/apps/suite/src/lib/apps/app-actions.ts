/**
 * App actions offered by registered apps (REQ-APPS-01..04, server
 * REQ-APP-50).
 *
 * The server advertises the actions in each account's
 * `accountCapabilities['https://netzhansa.com/jmap/apps']` as
 * `{ actions: [{appId, extensionId, point, appName, label, icon}] }`,
 * where `appName` and `label` are maps keyed by BCP 47 language tag with
 * `en` always present. This module turns that wire value into the list
 * the thread more-menu renders and holds the pure helpers of the invoke
 * flow (handoff URL, opened-URL scheme check).
 */
import { Capability, type SessionResource } from '../jmap/types';

/** Extension point of a per-thread action (server REQ-APP-22). */
export const THREAD_ACTION_POINT = 'thread.action';

/**
 * The closed app-action icon set (REQ-APPS-04). `app` is the generic
 * icon and the fallback for any name outside the set.
 */
export const APP_ACTION_ICONS = [
  'app',
  'gift',
  'ticket',
  'calendar-plus',
  'user-plus',
  'archive',
  'send',
  'link',
  'file-plus',
] as const;

export type AppActionIconName = (typeof APP_ACTION_ICONS)[number];

/** One action ready for rendering, resolved to the current language. */
export interface AppAction {
  appId: string;
  extensionId: string;
  point: string;
  label: string;
  appName: string;
  icon: AppActionIconName;
}

type SessionLike = Pick<SessionResource, 'accounts'> | null | undefined;

/** Map an icon name from the wire onto the closed set (REQ-APPS-04). */
export function normalizeAppActionIcon(icon: unknown): AppActionIconName {
  return typeof icon === 'string' && (APP_ACTION_ICONS as readonly string[]).includes(icon)
    ? (icon as AppActionIconName)
    : 'app';
}

/**
 * Pick the entry of a localized map for `lang`: the exact tag, then its
 * primary subtag (`de` for `de-CH`), then `en`, then any entry
 * (REQ-APPS-02).
 */
export function localizedText(map: unknown, lang: string): string {
  if (!map || typeof map !== 'object') return '';
  const m = map as Record<string, unknown>;
  const pick = (k: string): string | null => {
    const v = m[k];
    return typeof v === 'string' && v.length > 0 ? v : null;
  };
  const primary = lang.split('-')[0] ?? lang;
  const first = Object.values(m).find((v): v is string => typeof v === 'string' && v.length > 0);
  return pick(lang) ?? pick(primary) ?? pick('en') ?? first ?? '';
}

/**
 * The actions with point `point` offered on `accountId`, in capability
 * order (REQ-APPS-01). Empty when the account lacks the capability or
 * lists no such action. Entries missing their ids or a label are skipped.
 */
export function deriveAppActions(
  session: SessionLike,
  accountId: string | null | undefined,
  lang: string,
  point: string = THREAD_ACTION_POINT,
): AppAction[] {
  if (!session || !accountId) return [];
  const cap = session.accounts[accountId]?.accountCapabilities[Capability.HeroldApps] as
    | { actions?: unknown }
    | undefined;
  if (!cap || !Array.isArray(cap.actions)) return [];
  const out: AppAction[] = [];
  for (const raw of cap.actions as unknown[]) {
    if (!raw || typeof raw !== 'object') continue;
    const a = raw as Record<string, unknown>;
    if (a.point !== point) continue;
    if (typeof a.appId !== 'string' || typeof a.extensionId !== 'string') continue;
    const label = localizedText(a.label, lang);
    if (!label) continue;
    out.push({
      appId: a.appId,
      extensionId: a.extensionId,
      point,
      label,
      appName: localizedText(a.appName, lang),
      icon: normalizeAppActionIcon(a.icon),
    });
  }
  return out;
}

/** Key identifying one invocation target: the action on one thread. */
export function invocationKey(action: Pick<AppAction, 'appId' | 'extensionId'>, threadId: string): string {
  return `${action.appId}/${action.extensionId}/${threadId}`;
}

/** Hash route of the handoff placeholder tab (REQ-APPS-10). */
export const HANDOFF_ROUTE = '/app-handoff';

/**
 * Same-origin URL of the handoff tab for `appName`, carrying the name
 * and the language to render in as hash parameters.
 */
export function handoffUrl(base: { origin: string; pathname: string }, appName: string, lang: string): string {
  const qs = `app=${encodeURIComponent(appName)}&lang=${encodeURIComponent(lang)}`;
  return `${base.origin}${base.pathname}#${HANDOFF_ROUTE}?${qs}`;
}

/** Read the app name and language from a handoff tab's location hash. */
export function parseHandoffHash(hash: string): { appName: string; lang: string } {
  const q = hash.indexOf('?');
  const params = new URLSearchParams(q >= 0 ? hash.slice(q + 1) : '');
  return { appName: params.get('app') ?? '', lang: params.get('lang') ?? 'en' };
}

/** True when `hash` addresses the handoff route. */
export function isHandoffHash(hash: string): boolean {
  const path = hash.replace(/^#/, '').split('?')[0] ?? '';
  return path === HANDOFF_ROUTE;
}

/**
 * Whether the suite opens `url` handed back by `AppAction/invoke`
 * (REQ-APPS-11): `https:` always; `http:` only when `allowInsecureLoopback`
 * (dev builds) and the host is `localhost` or `127.0.0.1`.
 */
export function isOpenableAppUrl(url: string, allowInsecureLoopback: boolean): boolean {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return false;
  }
  if (parsed.protocol === 'https:') return true;
  if (parsed.protocol === 'http:' && allowInsecureLoopback) {
    return parsed.hostname === 'localhost' || parsed.hostname === '127.0.0.1';
  }
  return false;
}
