/**
 * AutosaveController tests (Item 7).
 *
 * The controller is the shared state machine behind the identity
 * editor's "Saving…/Saved" indicator: idle -> saving -> saved -> idle,
 * or saving -> error.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { AutosaveController } from './autosave.svelte';

describe('AutosaveController', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('starts idle', () => {
    const c = new AutosaveController();
    expect(c.state).toBe('idle');
    expect(c.errorMessage).toBeNull();
  });

  it('transitions saving -> saved on success and returns true', async () => {
    const c = new AutosaveController();
    let resolveFn: () => void = () => {};
    const pending = new Promise<void>((r) => {
      resolveFn = r;
    });
    const runP = c.run(() => pending);
    expect(c.state).toBe('saving');
    resolveFn();
    const result = await runP;
    expect(result).toBe(true);
    expect(c.state).toBe('saved');
  });

  it('auto-clears the saved state back to idle', async () => {
    const c = new AutosaveController();
    await c.run(async () => undefined);
    expect(c.state).toBe('saved');
    vi.advanceTimersByTime(2000);
    expect(c.state).toBe('idle');
  });

  it('transitions saving -> error on failure and returns false', async () => {
    const c = new AutosaveController();
    const result = await c.run(async () => {
      throw new Error('network down');
    });
    expect(result).toBe(false);
    expect(c.state).toBe('error');
    expect(c.errorMessage).toBe('network down');
  });

  it('a new run after an error clears the error', async () => {
    const c = new AutosaveController();
    await c.run(async () => {
      throw new Error('boom');
    });
    expect(c.state).toBe('error');
    await c.run(async () => undefined);
    expect(c.state).toBe('saved');
    expect(c.errorMessage).toBeNull();
  });

  it('dispose cancels the pending saved-clear timer', async () => {
    const c = new AutosaveController();
    await c.run(async () => undefined);
    expect(c.state).toBe('saved');
    c.dispose();
    vi.advanceTimersByTime(5000);
    // State stays 'saved' because the clear timer was cancelled.
    expect(c.state).toBe('saved');
  });
});
