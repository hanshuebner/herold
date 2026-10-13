import { describe, it, expect, vi, beforeEach } from 'vitest';

vi.mock('../i18n/i18n.svelte', () => ({
  i18n: { locale: 'en' },
  t: (key: string, params?: Record<string, string | number>) =>
    params ? `${key}(${Object.entries(params).map(([k, v]) => `${k}=${v}`).join(',')})` : key,
}));
vi.mock('../auth/auth.svelte', () => ({ auth: { refreshSession: vi.fn() } }));
vi.mock('../jmap/client', () => ({ jmap: {}, strict: (r: unknown) => r }));

import { AppInvoker, type AppInvokerDeps, type HandoffWindow, type InvokeResult } from './app-invoke.svelte';
import { JmapMethodError } from '../jmap/errors';
import type { ToastSpec } from '../toast/toast.svelte';
import type { AppAction } from './app-actions';

const action: AppAction = {
  appId: '1',
  extensionId: 'open-url',
  point: 'thread.action',
  label: 'Open in fake app',
  appName: 'Fake app',
  icon: 'link',
};
const target = { accountId: 'A1', threadId: 'T1' };

function fakeWindow() {
  const replace = vi.fn<(url: string) => void>();
  const close = vi.fn<() => void>();
  const win: HandoffWindow = { opener: {}, location: { replace }, close };
  return Object.assign(win, { replace, close });
}

function setup(opts: { win?: HandoffWindow | null; result?: Promise<InvokeResult> } = {}) {
  const win = opts.win === undefined ? fakeWindow() : opts.win;
  const toasts: ToastSpec[] = [];
  const deps: AppInvokerDeps = {
    openWindow: vi.fn(() => win),
    callInvoke: vi.fn(() => opts.result ?? Promise.resolve({ type: 'notice', text: 'ok' } as InvokeResult)),
    showToast: vi.fn((s: ToastSpec) => {
      toasts.push(s);
    }),
    refreshSession: vi.fn(() => Promise.resolve()),
    location: () => ({ origin: 'https://mail.example', pathname: '/' }),
    lang: () => 'en',
    allowInsecureLoopback: false,
  };
  return { invoker: new AppInvoker(deps), deps, win, toasts };
}

function methodError(type: string, description?: string): JmapMethodError {
  return new JmapMethodError('c0', { type, description });
}

describe('AppInvoker', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('opens the handoff tab before invoking, with the app name in the URL', async () => {
    const { invoker, deps } = setup();
    const p = invoker.invoke(action, target);
    expect(deps.openWindow).toHaveBeenCalledWith('https://mail.example/#/app-handoff?app=Fake%20app&lang=en');
    await p;
    expect(deps.callInvoke).toHaveBeenCalledWith({
      accountId: 'A1',
      appId: '1',
      extensionId: 'open-url',
      threadId: 'T1',
    });
  });

  it('cuts the opener and navigates the tab on openUrl', async () => {
    const win = fakeWindow();
    const { invoker, toasts } = setup({
      win,
      result: Promise.resolve({ type: 'openUrl', url: 'https://app.example/opened/42' }),
    });
    await invoker.invoke(action, target);
    expect(win.opener).toBeNull();
    expect(win.replace).toHaveBeenCalledWith('https://app.example/opened/42');
    expect(win.close).not.toHaveBeenCalled();
    expect(toasts).toEqual([]);
  });

  it('refuses an openUrl with a scheme other than https', async () => {
    const win = fakeWindow();
    const { invoker, toasts } = setup({
      win,
      result: Promise.resolve({ type: 'openUrl', url: 'http://127.0.0.1:9000/opened/42' }),
    });
    await invoker.invoke(action, target);
    expect(win.replace).not.toHaveBeenCalled();
    expect(win.close).toHaveBeenCalled();
    expect(toasts[0]).toMatchObject({ kind: 'error', message: 'apps.error.badUrl(app=Fake app)' });
  });

  it('closes the tab and shows the notice text', async () => {
    const win = fakeWindow();
    const { invoker, toasts } = setup({ win, result: Promise.resolve({ type: 'notice', text: 'Got 2 messages' }) });
    await invoker.invoke(action, target);
    expect(win.close).toHaveBeenCalled();
    expect(toasts[0]).toMatchObject({ kind: 'info', message: 'Got 2 messages' });
  });

  it.each([
    [methodError('appRejected', 'No thanks.'), 'apps.error.rejected(app=Fake app,text=No thanks.)', false],
    [methodError('appRejected'), 'apps.error.rejectedNoText(app=Fake app)', false],
    [methodError('appUnavailable'), 'apps.error.unavailable(app=Fake app)', false],
    [methodError('tooLarge'), 'apps.error.tooLarge(app=Fake app)', false],
    [methodError('notFound'), 'apps.error.gone', true],
    [methodError('forbidden'), 'apps.error.gone', true],
    [new Error('network down'), 'apps.error.failed(app=Fake app)', false],
  ])('closes the tab and shows an error toast for %s', async (err, message, refetch) => {
    const win = fakeWindow();
    const { invoker, toasts, deps } = setup({ win, result: Promise.reject(err) });
    await invoker.invoke(action, target);
    expect(win.close).toHaveBeenCalled();
    expect(win.replace).not.toHaveBeenCalled();
    expect(toasts[0]).toMatchObject({ kind: 'error', message });
    expect(deps.refreshSession).toHaveBeenCalledTimes(refetch ? 1 : 0);
  });

  it('does not invoke when the pop-up was blocked and offers Try again', async () => {
    const { invoker, deps, toasts } = setup({ win: null });
    await invoker.invoke(action, target);
    expect(deps.callInvoke).not.toHaveBeenCalled();
    expect(toasts[0]).toMatchObject({
      kind: 'error',
      message: 'apps.popupBlocked(app=Fake app)',
      actionLabel: 'apps.tryAgain',
    });

    vi.mocked(deps.openWindow).mockReturnValue(fakeWindow());
    toasts[0]?.undo?.();
    expect(deps.openWindow).toHaveBeenCalledTimes(2);
    await vi.waitFor(() => expect(deps.callInvoke).toHaveBeenCalledOnce());
  });

  it('is busy while in flight and ignores a second activation', async () => {
    let resolve!: (r: InvokeResult) => void;
    const pending = new Promise<InvokeResult>((r) => {
      resolve = r;
    });
    const { invoker, deps } = setup({ result: pending });
    const first = invoker.invoke(action, target);
    expect(invoker.isBusy(action, 'T1')).toBe(true);
    expect(invoker.isBusy(action, 'T2')).toBe(false);

    await invoker.invoke(action, target);
    expect(deps.openWindow).toHaveBeenCalledOnce();
    expect(deps.callInvoke).toHaveBeenCalledOnce();

    resolve({ type: 'notice', text: 'done' });
    await first;
    expect(invoker.isBusy(action, 'T1')).toBe(false);
  });

  it('runs the same action on another thread concurrently', async () => {
    const { invoker, deps } = setup({ result: new Promise(() => {}) });
    void invoker.invoke(action, target);
    void invoker.invoke(action, { accountId: 'A1', threadId: 'T2' });
    expect(deps.callInvoke).toHaveBeenCalledTimes(2);
  });
});
