/**
 * App-action invocation (REQ-APPS-10..16).
 *
 * `invoke()` runs inside the menu item's click handler. It opens the
 * same-origin handoff tab synchronously, so popup blockers treat the tab
 * as user-initiated, then calls `AppAction/invoke` on the backend and
 * resolves the tab:
 *
 *   openUrl  -> the tab's `opener` is cut and the tab navigates to the URL
 *   notice   -> the tab closes; the text shows in an info toast
 *   error    -> the tab closes; an error toast names the cause
 *
 * A blocked tab (`window.open` returned null) skips the invocation and
 * shows a toast whose "Try again" button repeats the whole click path.
 * One invocation per action and thread runs at a time; the key stays in
 * `#inFlight` until the call settles, which drives the menu spinner. The
 * invocation is independent of the view, so leaving the thread does not
 * cancel it. Nothing in the mailbox changes, so no undo is offered.
 */
import { SvelteSet } from 'svelte/reactivity';
import { jmap, strict } from '../jmap/client';
import { JmapMethodError } from '../jmap/errors';
import { Capability } from '../jmap/types';
import { toast, type ToastSpec } from '../toast/toast.svelte';
import { auth } from '../auth/auth.svelte';
import { i18n, t } from '../i18n/i18n.svelte';
import { handoffUrl, invocationKey, isOpenableAppUrl, type AppAction } from './app-actions';

/** Result of `AppAction/invoke` (server REQ-APP-51). */
export type InvokeResult = { type: 'openUrl'; url: string } | { type: 'notice'; text: string };

export interface InvokeTarget {
  accountId: string;
  threadId: string;
}

/** The minimal window surface the flow touches. */
export interface HandoffWindow {
  opener: unknown;
  location: { replace(url: string): void };
  close(): void;
}

export interface AppInvokerDeps {
  openWindow(url: string): HandoffWindow | null;
  callInvoke(args: {
    accountId: string;
    appId: string;
    extensionId: string;
    threadId: string;
  }): Promise<InvokeResult>;
  showToast(spec: ToastSpec): void;
  refreshSession(): Promise<void>;
  /** Origin and path the handoff URL is built on. */
  location(): { origin: string; pathname: string };
  lang(): string;
  /** True in dev builds: `http:` URLs on loopback hosts may be opened. */
  allowInsecureLoopback: boolean;
}

const ERROR_TOAST_MS = 8000;

/** Map a failed invocation onto the toast message of REQ-APPS-13. */
export function invokeErrorMessage(err: unknown, appName: string): string {
  if (err instanceof JmapMethodError) {
    const { type, description } = err.methodError;
    switch (type) {
      case 'appRejected':
        return description
          ? t('apps.error.rejected', { app: appName, text: description })
          : t('apps.error.rejectedNoText', { app: appName });
      case 'appUnavailable':
        return t('apps.error.unavailable', { app: appName });
      case 'tooLarge':
        return t('apps.error.tooLarge', { app: appName });
      case 'notFound':
      case 'forbidden':
        return t('apps.error.gone');
    }
  }
  return t('apps.error.failed', { app: appName });
}

function isGoneError(err: unknown): boolean {
  return (
    err instanceof JmapMethodError &&
    (err.methodError.type === 'notFound' || err.methodError.type === 'forbidden')
  );
}

export class AppInvoker {
  #deps: AppInvokerDeps;
  #inFlight = new SvelteSet<string>();

  constructor(deps: AppInvokerDeps) {
    this.#deps = deps;
  }

  /** True while an invocation of `action` on `threadId` is running. */
  isBusy(action: Pick<AppAction, 'appId' | 'extensionId'>, threadId: string): boolean {
    return this.#inFlight.has(invocationKey(action, threadId));
  }

  /**
   * Start the invocation. Must be called synchronously from a user
   * gesture. Resolves once the handoff tab has been resolved; a call
   * for an action already in flight on the same thread returns at once.
   */
  invoke(action: AppAction, target: InvokeTarget): Promise<void> {
    const key = invocationKey(action, target.threadId);
    if (this.#inFlight.has(key)) return Promise.resolve();
    const d = this.#deps;
    const appName = action.appName || action.label;
    const win = d.openWindow(handoffUrl(d.location(), appName, d.lang()));
    if (!win) {
      d.showToast({
        kind: 'error',
        message: t('apps.popupBlocked', { app: appName }),
        actionLabel: t('apps.tryAgain'),
        undo: () => {
          void this.invoke(action, target);
        },
        timeoutMs: 0,
      });
      return Promise.resolve();
    }
    this.#inFlight.add(key);
    return this.#run(action, target, appName, win).finally(() => {
      this.#inFlight.delete(key);
    });
  }

  async #run(action: AppAction, target: InvokeTarget, appName: string, win: HandoffWindow): Promise<void> {
    const d = this.#deps;
    let result: InvokeResult;
    try {
      result = await d.callInvoke({
        accountId: target.accountId,
        appId: action.appId,
        extensionId: action.extensionId,
        threadId: target.threadId,
      });
    } catch (err) {
      win.close();
      d.showToast({ kind: 'error', message: invokeErrorMessage(err, appName), timeoutMs: ERROR_TOAST_MS });
      if (isGoneError(err)) void d.refreshSession();
      return;
    }
    if (result.type === 'openUrl') {
      if (!isOpenableAppUrl(result.url, d.allowInsecureLoopback)) {
        win.close();
        d.showToast({ kind: 'error', message: t('apps.error.badUrl', { app: appName }), timeoutMs: ERROR_TOAST_MS });
        return;
      }
      win.opener = null;
      win.location.replace(result.url);
      return;
    }
    win.close();
    d.showToast({ kind: 'info', message: result.text, timeoutMs: ERROR_TOAST_MS });
  }
}

/** `AppAction/invoke` over the suite's JMAP client. */
export async function callAppActionInvoke(args: {
  accountId: string;
  appId: string;
  extensionId: string;
  threadId: string;
}): Promise<InvokeResult> {
  const { responses } = await jmap.batch((b) => {
    b.call('AppAction/invoke', args, [Capability.Core, Capability.HeroldApps]);
  });
  const [inv] = strict(responses);
  const res = (inv?.[1] ?? {}) as { type?: string; url?: string; text?: string };
  if (res.type === 'openUrl' && typeof res.url === 'string') return { type: 'openUrl', url: res.url };
  if (res.type === 'notice') return { type: 'notice', text: typeof res.text === 'string' ? res.text : '' };
  throw new Error(`AppAction/invoke: unexpected response type ${String(res.type)}`);
}

export const appInvoker = new AppInvoker({
  openWindow: (url) => window.open(url, '_blank'),
  callInvoke: callAppActionInvoke,
  showToast: (spec) => toast.show(spec),
  refreshSession: () => auth.refreshSession(),
  location: () => ({ origin: window.location.origin, pathname: window.location.pathname }),
  lang: () => i18n.locale,
  allowInsecureLoopback: import.meta.env.DEV,
});
