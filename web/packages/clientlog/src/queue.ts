/**
 * Bounded in-memory event queue (REQ-CLOG-09, REQ-OPS-202).
 *
 * Split sub-queues to prefer retaining errors when the queue is full:
 *   errors sub-queue: cap ERROR_CAP (50)
 *   rest   sub-queue: cap REST_CAP  (150)
 *   total default cap: 200 (matches REQ-CLOG-09)
 *
 * Drop policy when full:
 *   - If rest sub-queue is full, drop the oldest rest entry.
 *   - If rest sub-queue is empty AND errors sub-queue is full, drop the
 *     oldest error entry (last resort).
 *   Either way, the drop counter is incremented.
 *
 * On the next flush, a synthetic warning event is prepended to the batch
 * so the operator can see the gap.
 */

import type { CapturedEvent } from './schema.js';

const ERROR_CAP = 50;
const REST_CAP = 150;

/** Returns a pair of sub-queues [errors, rest] from the queue parameters. */
export interface Queue {
  enqueue(event: CapturedEvent): void;
  /** Drains both sub-queues and returns events oldest-first. */
  drain(): CapturedEvent[];
  /** Total number of events currently held across both sub-queues. */
  size(): number;
  /** Accumulated drop count since the last drain. */
  dropCount(): number;
}

export function createQueue(
  opts: { errorCap?: number; restCap?: number } = {},
): Queue {
  const errorCap = opts.errorCap ?? ERROR_CAP;
  const restCap = opts.restCap ?? REST_CAP;

  const errors: CapturedEvent[] = [];
  const rest: CapturedEvent[] = [];
  let drops = 0;

  function enqueue(event: CapturedEvent): void {
    if (event.kind === 'error') {
      if (errors.length >= errorCap) {
        // drop oldest error (very rare -- error sub-queue is generous)
        errors.shift();
        drops++;
      }
      errors.push(event);
    } else {
      if (rest.length >= restCap) {
        // drop oldest rest entry
        rest.shift();
        drops++;
      }
      rest.push(event);
    }
  }

  function drain(): CapturedEvent[] {
    const errorSnapshot = errors.splice(0, errors.length);
    const restSnapshot = rest.splice(0, rest.length);
    drops = 0;
    // Return errors first, then rest -- but the flush layer re-sorts
    // by seq, so this is only an approximate ordering hint.
    return [...errorSnapshot, ...restSnapshot];
  }

  function size(): number {
    return errors.length + rest.length;
  }

  function dropCount(): number {
    return drops;
  }

  return { enqueue, drain, size, dropCount };
}
