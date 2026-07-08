/**
 * Unit tests for SW update detection helpers — REQ-PUSH-72 / REQ-MOB-75.
 *
 * Coverage:
 *   shouldShowBanner: four combinations of waiting/controller nullness.
 *   watchRegistration: already-waiting check (update vs first-install),
 *     updatefound -> statechange -> 'installed' (update vs first-install).
 *   activateWaiting: no-op when waiting is null, posts SKIP_WAITING and
 *     calls onReload exactly once via the controllerchange guard.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { _internals_forTest } from '../sw-update.js';

const {
  shouldShowBanner,
  watchRegistration,
  activateWaiting,
  startPeriodicUpdateCheck,
  resetReloading,
} = _internals_forTest;

// ── Helpers ────────────────────────────────────────────────────────────────

function mockSw(): ServiceWorker {
  return {} as ServiceWorker;
}

type StateChangeListener = () => void;

interface MockInstalling {
  state: string;
  _stateListeners: StateChangeListener[];
  addEventListener(type: string, cb: () => void): void;
  dispatchStateChange(state: string): void;
}

type UpdateFoundListener = () => void;

interface MockRegistration {
  waiting: ServiceWorker | null;
  installing: MockInstalling | null;
  _updateFoundListeners: UpdateFoundListener[];
  addEventListener(type: string, cb: () => void): void;
  dispatchUpdateFound(): void;
}

function makeInstalling(): MockInstalling {
  const inst: MockInstalling = {
    state: 'installing',
    _stateListeners: [],
    addEventListener(type: string, cb: () => void) {
      if (type === 'statechange') inst._stateListeners.push(cb);
    },
    dispatchStateChange(state: string) {
      inst.state = state;
      for (const cb of inst._stateListeners) cb();
    },
  };
  return inst;
}

function makeRegistration(opts: {
  waiting?: ServiceWorker | null;
  installing?: MockInstalling | null;
} = {}): MockRegistration {
  const reg: MockRegistration = {
    waiting: opts.waiting ?? null,
    installing: opts.installing ?? null,
    _updateFoundListeners: [],
    addEventListener(type: string, cb: () => void) {
      if (type === 'updatefound') reg._updateFoundListeners.push(cb);
    },
    dispatchUpdateFound() {
      for (const cb of reg._updateFoundListeners) cb();
    },
  };
  return reg;
}

// ── shouldShowBanner ───────────────────────────────────────────────────────

describe('shouldShowBanner', () => {
  it('returns false when no waiting SW and no controller', () => {
    expect(shouldShowBanner(null, null)).toBe(false);
  });

  it('returns false when waiting SW but no controller (first install)', () => {
    expect(shouldShowBanner(mockSw(), null)).toBe(false);
  });

  it('returns false when controller exists but no waiting SW', () => {
    expect(shouldShowBanner(null, mockSw())).toBe(false);
  });

  it('returns true when both waiting SW and controller are present (update)', () => {
    expect(shouldShowBanner(mockSw(), mockSw())).toBe(true);
  });
});

// ── watchRegistration — already-waiting ───────────────────────────────────

describe('watchRegistration: already-waiting case', () => {
  it('calls onShow immediately when waiting is set and controller exists', () => {
    const onShow = vi.fn();
    const controller = mockSw();
    const reg = makeRegistration({ waiting: mockSw() });

    watchRegistration(
      reg as unknown as ServiceWorkerRegistration,
      () => controller,
      onShow,
    );

    expect(onShow).toHaveBeenCalledTimes(1);
  });

  it('does NOT call onShow on first install (no controller)', () => {
    const onShow = vi.fn();
    const reg = makeRegistration({ waiting: mockSw() });

    watchRegistration(
      reg as unknown as ServiceWorkerRegistration,
      () => null,
      onShow,
    );

    expect(onShow).not.toHaveBeenCalled();
  });

  it('does NOT call onShow when there is no waiting SW', () => {
    const onShow = vi.fn();
    const controller = mockSw();
    const reg = makeRegistration({ waiting: null });

    watchRegistration(
      reg as unknown as ServiceWorkerRegistration,
      () => controller,
      onShow,
    );

    expect(onShow).not.toHaveBeenCalled();
  });

  it('suppresses the initial already-waiting prompt right after an update reload', () => {
    const onShow = vi.fn();
    const controller = mockSw();
    const reg = makeRegistration({ waiting: mockSw() });

    watchRegistration(
      reg as unknown as ServiceWorkerRegistration,
      () => controller,
      onShow,
      { suppressInitialWaiting: true },
    );

    // The version we just activated is not re-announced.
    expect(onShow).not.toHaveBeenCalled();
  });

  it('suppresses the first updatefound after a post-activation reload and announces the second', () => {
    // The first updatefound is the browser's spurious re-check of the just-
    // activated sw.js version (the update-check races with SW activation).
    // The second updatefound is a genuine new deploy (e.g. from the hourly
    // startPeriodicUpdateCheck) and must still be announced.
    const onShow = vi.fn();
    const controller = mockSw();
    const spuriousInstalling = makeInstalling();
    const genuineInstalling = makeInstalling();
    const reg = makeRegistration({ waiting: mockSw(), installing: spuriousInstalling });

    watchRegistration(
      reg as unknown as ServiceWorkerRegistration,
      () => controller,
      onShow,
      { suppressInitialWaiting: true },
    );
    expect(onShow).not.toHaveBeenCalled();

    // First updatefound: spurious register()-triggered check for the version
    // we just activated.  Must NOT show the banner.
    reg.dispatchUpdateFound();
    spuriousInstalling.dispatchStateChange('installed');
    expect(onShow).not.toHaveBeenCalled();

    // Second updatefound: a genuinely new version installed later.
    reg.installing = genuineInstalling;
    reg.dispatchUpdateFound();
    genuineInstalling.dispatchStateChange('installed');
    expect(onShow).toHaveBeenCalledTimes(1);
  });
});

// ── Regression: post-activation reload double-prompt ─────────────────────────

describe('watchRegistration: post-activation reload double-prompt regression', () => {
  it('does not re-show banner when updatefound fires on the post-activation reloaded page', () => {
    // REGRESSION TEST (re #N): clicking "Neu laden" reloads the page; the
    // reloaded page ran register() and the browser's update check raced with
    // SW activation, firing updatefound for the version we just activated.
    // suppressInitialWaiting=true was set from sessionStorage but previously
    // only suppressed Case 1 (reg.waiting non-null at call time).  Case 2
    // (updatefound -> statechange -> installed) was never suppressed, so the
    // banner reappeared immediately after reload.
    //
    // Sequence modelled here:
    //   Page 1: Version A active, Version B waiting -> Case 1 -> banner shown.
    //   User clicks reload: SKIP_WAITING, controllerchange, sessionStorage set,
    //   location.reload().
    //   Page 2 (this test): Version B is now controller.  register() fires an
    //   update check; browser compares fetched sw.js against the not-yet-fully-
    //   committed installed state and fires updatefound for the same Version B
    //   bytes.  Banner must NOT reappear.
    const onShow = vi.fn();
    const controllerB = mockSw(); // Version B just became the controller.
    const installing = makeInstalling();
    // reg.waiting is null: Version B is active, not waiting.
    const reg = makeRegistration({ waiting: null, installing });

    watchRegistration(
      reg as unknown as ServiceWorkerRegistration,
      () => controllerB,
      onShow,
      { suppressInitialWaiting: true }, // set from sessionStorage on reload
    );

    // Case 1: reg.waiting is null -> no immediate banner (correct).
    expect(onShow).not.toHaveBeenCalled();

    // Spurious Case 2: the update check for register() fires updatefound
    // and the installing SW reaches installed state.  This is Version B bytes
    // again -- must NOT prompt.
    reg.dispatchUpdateFound();
    installing.dispatchStateChange('installed');
    expect(onShow).not.toHaveBeenCalled();
  });

  it('does not re-show banner even when reg.waiting is transiently non-null on the reloaded page', () => {
    // Models candidate (a)/(b): if reg.waiting is briefly non-null on the
    // reloaded page (SW still in the INSTALLED->ACTIVATING transition at the
    // time register() resolves), Case 1 is correctly suppressed by
    // suppressInitialWaiting.
    const onShow = vi.fn();
    const controllerB = mockSw();
    const reg = makeRegistration({ waiting: mockSw() }); // transient waiting

    watchRegistration(
      reg as unknown as ServiceWorkerRegistration,
      () => controllerB,
      onShow,
      { suppressInitialWaiting: true },
    );

    expect(onShow).not.toHaveBeenCalled();
  });
});

// ── watchRegistration — updatefound -> statechange ────────────────────────

describe('watchRegistration: updatefound -> statechange', () => {
  it('calls onShow when installing reaches installed and controller exists', () => {
    const onShow = vi.fn();
    const controller = mockSw();
    const installing = makeInstalling();
    const reg = makeRegistration({ installing });

    watchRegistration(
      reg as unknown as ServiceWorkerRegistration,
      () => controller,
      onShow,
    );
    reg.dispatchUpdateFound();
    installing.dispatchStateChange('installed');

    expect(onShow).toHaveBeenCalledTimes(1);
  });

  it('does NOT call onShow on first install (controller null at statechange time)', () => {
    const onShow = vi.fn();
    const installing = makeInstalling();
    const reg = makeRegistration({ installing });

    watchRegistration(
      reg as unknown as ServiceWorkerRegistration,
      () => null,
      onShow,
    );
    reg.dispatchUpdateFound();
    installing.dispatchStateChange('installed');

    expect(onShow).not.toHaveBeenCalled();
  });

  it('does NOT call onShow for intermediate states (installing, activating)', () => {
    const onShow = vi.fn();
    const controller = mockSw();
    const installing = makeInstalling();
    const reg = makeRegistration({ installing });

    watchRegistration(
      reg as unknown as ServiceWorkerRegistration,
      () => controller,
      onShow,
    );
    reg.dispatchUpdateFound();
    installing.dispatchStateChange('installing');
    installing.dispatchStateChange('activating');

    expect(onShow).not.toHaveBeenCalled();
  });

  it('does nothing when updatefound fires but installing is null', () => {
    const onShow = vi.fn();
    const controller = mockSw();
    const reg = makeRegistration({ installing: null });

    watchRegistration(
      reg as unknown as ServiceWorkerRegistration,
      () => controller,
      onShow,
    );
    reg.dispatchUpdateFound();

    expect(onShow).not.toHaveBeenCalled();
  });
});

// ── activateWaiting ───────────────────────────────────────────────────────

describe('activateWaiting', () => {
  beforeEach(() => {
    resetReloading();
  });

  it('does nothing when waiting is null', () => {
    const onReload = vi.fn();
    const addListener = vi.fn();

    activateWaiting(null, addListener, onReload);

    expect(addListener).not.toHaveBeenCalled();
    expect(onReload).not.toHaveBeenCalled();
  });

  it('posts SKIP_WAITING to the waiting worker', () => {
    const waiting = { postMessage: vi.fn() } as unknown as ServiceWorker;
    const onReload = vi.fn();
    let capturedCb: (() => void) | undefined;

    activateWaiting(
      waiting,
      (cb) => { capturedCb = cb; },
      onReload,
    );

    expect(waiting.postMessage).toHaveBeenCalledWith({ type: 'SKIP_WAITING' });
    expect(capturedCb).toBeDefined();
  });

  it('calls onReload once when controllerchange fires', () => {
    const waiting = { postMessage: vi.fn() } as unknown as ServiceWorker;
    const onReload = vi.fn();
    let capturedCb: (() => void) | undefined;

    activateWaiting(
      waiting,
      (cb) => { capturedCb = cb; },
      onReload,
    );

    capturedCb?.();
    expect(onReload).toHaveBeenCalledTimes(1);
  });

  it('calls onReload at most once even if controllerchange fires multiple times', () => {
    const waiting = { postMessage: vi.fn() } as unknown as ServiceWorker;
    const onReload = vi.fn();
    const listeners: Array<() => void> = [];

    activateWaiting(
      waiting,
      (cb) => { listeners.push(cb); },
      onReload,
    );

    // Simulate controllerchange firing three times.
    listeners[0]?.();
    listeners[0]?.();
    listeners[0]?.();

    expect(onReload).toHaveBeenCalledTimes(1);
  });
});

// ── startPeriodicUpdateCheck ──────────────────────────────────────────────

describe('startPeriodicUpdateCheck', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('does not call reg.update() before the first interval', () => {
    const reg = { update: vi.fn().mockResolvedValue(undefined) } as unknown as ServiceWorkerRegistration;

    startPeriodicUpdateCheck(reg, 1000);

    vi.advanceTimersByTime(999);
    expect(reg.update).not.toHaveBeenCalled();
  });

  it('calls reg.update() after the first interval elapses', () => {
    const reg = { update: vi.fn().mockResolvedValue(undefined) } as unknown as ServiceWorkerRegistration;

    startPeriodicUpdateCheck(reg, 1000);

    vi.advanceTimersByTime(1000);
    expect(reg.update).toHaveBeenCalledTimes(1);
  });

  it('calls reg.update() on each subsequent interval tick', () => {
    const reg = { update: vi.fn().mockResolvedValue(undefined) } as unknown as ServiceWorkerRegistration;

    startPeriodicUpdateCheck(reg, 1000);

    vi.advanceTimersByTime(3000);
    expect(reg.update).toHaveBeenCalledTimes(3);
  });

  it('returns a cleanup function that stops the interval', () => {
    const reg = { update: vi.fn().mockResolvedValue(undefined) } as unknown as ServiceWorkerRegistration;

    const stop = startPeriodicUpdateCheck(reg, 1000);
    vi.advanceTimersByTime(1000);
    expect(reg.update).toHaveBeenCalledTimes(1);

    stop();
    vi.advanceTimersByTime(5000);
    // No further calls after cleanup.
    expect(reg.update).toHaveBeenCalledTimes(1);
  });

  it('swallows update() rejections without propagating', async () => {
    const reg = {
      update: vi.fn().mockRejectedValue(new Error('network error')),
    } as unknown as ServiceWorkerRegistration;

    startPeriodicUpdateCheck(reg, 1000);
    vi.advanceTimersByTime(1000);

    // Flush microtasks so the .catch() handler inside startPeriodicUpdateCheck
    // can settle before we assert.  A single Promise.resolve() turn is enough.
    await Promise.resolve();

    // The rejection was caught; no unhandled rejection, and the interval ran.
    expect(reg.update).toHaveBeenCalledTimes(1);
  });
});
