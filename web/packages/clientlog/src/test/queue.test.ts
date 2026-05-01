/**
 * Tests for the bounded queue (REQ-CLOG-09).
 *
 * Covers:
 *   - Error sub-queue cap and drop-oldest-with-error-retention.
 *   - Rest sub-queue cap and drop-oldest.
 *   - Drop counter increments and resets on drain.
 *   - Mixed drop scenario: rest fills first, then errors.
 */

import { describe, it, expect } from 'vitest';
import { createQueue } from '../queue.js';
import type { CapturedEvent } from '../schema.js';

function makeEvent(kind: 'error' | 'log' | 'vital', seq: number): CapturedEvent {
  return {
    kind,
    level: kind === 'error' ? 'error' : 'info',
    msg: `msg-${seq}`,
    client_ts: '2024-01-01T00:00:00.000Z',
    seq,
  };
}

describe('queue -- error sub-queue cap', () => {
  it('retains errors when error sub-queue is full by dropping oldest error', () => {
    const q = createQueue({ errorCap: 3, restCap: 10 });
    for (let i = 0; i < 4; i++) q.enqueue(makeEvent('error', i));
    expect(q.size()).toBe(3);
    expect(q.dropCount()).toBe(1);
    // seq 0 was dropped; seq 1..3 remain
    const events = q.drain();
    expect(events.map((e) => e.seq)).toEqual([1, 2, 3]);
  });

  it('does not drop errors when under cap', () => {
    const q = createQueue({ errorCap: 5, restCap: 10 });
    for (let i = 0; i < 5; i++) q.enqueue(makeEvent('error', i));
    expect(q.size()).toBe(5);
    expect(q.dropCount()).toBe(0);
  });
});

describe('queue -- rest sub-queue cap', () => {
  it('drops oldest rest entries when full', () => {
    const q = createQueue({ errorCap: 10, restCap: 3 });
    for (let i = 0; i < 5; i++) q.enqueue(makeEvent('log', i));
    expect(q.size()).toBe(3);
    expect(q.dropCount()).toBe(2);
    const events = q.drain();
    expect(events.map((e) => e.seq)).toEqual([2, 3, 4]);
  });

  it('does not drop rest when under cap', () => {
    const q = createQueue({ errorCap: 10, restCap: 5 });
    for (let i = 0; i < 5; i++) q.enqueue(makeEvent('log', i));
    expect(q.size()).toBe(5);
    expect(q.dropCount()).toBe(0);
  });
});

describe('queue -- drain resets drop counter', () => {
  it('resets drop counter to 0 after drain', () => {
    const q = createQueue({ errorCap: 2, restCap: 2 });
    for (let i = 0; i < 4; i++) q.enqueue(makeEvent('log', i));
    expect(q.dropCount()).toBe(2);
    q.drain();
    expect(q.dropCount()).toBe(0);
    expect(q.size()).toBe(0);
  });
});

describe('queue -- mixed errors and rest', () => {
  it('errors and rest coexist; drain returns both', () => {
    const q = createQueue({ errorCap: 5, restCap: 5 });
    q.enqueue(makeEvent('error', 1));
    q.enqueue(makeEvent('log', 2));
    q.enqueue(makeEvent('vital', 3));
    q.enqueue(makeEvent('error', 4));
    expect(q.size()).toBe(4);
    const events = q.drain();
    // errors first (implementation detail), then rest
    const errorEvents = events.filter((e) => e.kind === 'error');
    const restEvents = events.filter((e) => e.kind !== 'error');
    expect(errorEvents).toHaveLength(2);
    expect(restEvents).toHaveLength(2);
  });

  it('rest fills first when mixed queue is full, errors retained', () => {
    const q = createQueue({ errorCap: 5, restCap: 2 });
    // Fill rest
    q.enqueue(makeEvent('log', 1));
    q.enqueue(makeEvent('log', 2));
    // Next log drops oldest rest
    q.enqueue(makeEvent('log', 3));
    expect(q.dropCount()).toBe(1);
    // Error still fits
    q.enqueue(makeEvent('error', 4));
    expect(q.dropCount()).toBe(1);
    expect(q.size()).toBe(3);
  });
});

describe('queue -- drain clears queue', () => {
  it('size is 0 after drain', () => {
    const q = createQueue({ errorCap: 5, restCap: 5 });
    q.enqueue(makeEvent('log', 1));
    q.enqueue(makeEvent('error', 2));
    q.drain();
    expect(q.size()).toBe(0);
  });
});
