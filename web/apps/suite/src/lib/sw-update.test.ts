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

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { _internals_forTest } from './sw-update';

const {
  shouldShowBanner,
  watchRegistration,
  activateWaiting,
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
