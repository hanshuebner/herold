/**
 * Tests for the confirm dialog store. Covers ask/decide round-trips,
 * cancellation of an outstanding ask when a new ask comes in, and the
 * default-cancel-on-cancel-via-decide(false) path.
 */
import { describe, it, expect, beforeEach } from 'vitest';
import { confirm } from './confirm.svelte';

beforeEach(() => {
  // Reset the store between tests. ask() rejects the prior pending so
  // we don't leak promises across cases.
  if (confirm.pending) {
    confirm.decide(false);
  }
});

describe('confirm.ask', () => {
  it('resolves true when decide(true) is called', async () => {
    const promise = confirm.ask({ message: 'do it?' });
    expect(confirm.pending?.message).toBe('do it?');
    confirm.decide(true);
    await expect(promise).resolves.toBe(true);
    expect(confirm.pending).toBeNull();
  });

  it('resolves false when decide(false) is called', async () => {
    const promise = confirm.ask({ message: 'do it?' });
    confirm.decide(false);
    await expect(promise).resolves.toBe(false);
    expect(confirm.pending).toBeNull();
  });

  it('cancels the prior pending ask when a new ask comes in', async () => {
    const first = confirm.ask({ message: 'first' });
    const second = confirm.ask({ message: 'second' });
    // The first promise should resolve to false because the second
    // ask supplanted it.
    await expect(first).resolves.toBe(false);
    expect(confirm.pending?.message).toBe('second');
    confirm.decide(true);
    await expect(second).resolves.toBe(true);
  });

  it('decide is a no-op when nothing is pending', () => {
    confirm.decide(true);
    expect(confirm.pending).toBeNull();
  });

  it('preserves the optional fields on the pending request', () => {
    void confirm.ask({
      message: 'm',
      title: 't',
      confirmLabel: 'ok',
      cancelLabel: 'no',
      kind: 'danger',
    });
    expect(confirm.pending?.title).toBe('t');
    expect(confirm.pending?.confirmLabel).toBe('ok');
    expect(confirm.pending?.cancelLabel).toBe('no');
    expect(confirm.pending?.kind).toBe('danger');
  });
});
