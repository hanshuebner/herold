/**
 * Livetail observer (REQ-CLOG-05, REQ-OPS-211).
 *
 * Polls cfg.livetailUntil() every 1 second. When the timestamp is in the
 * future, flush policy switches to 'aggressive' (100 ms timer + synchronous
 * per-event). When it expires or becomes null, policy reverts to 'normal'.
 */

export type FlushPolicy = 'normal' | 'aggressive';

export interface LivetailWatcher {
  stop(): void;
  /** Current flush policy, read by the flush layer. */
  policy(): FlushPolicy;
}

export function startLivetailWatcher(
  livetailUntil: () => number | null,
  now: () => number = Date.now.bind(Date),
  setIntervalFn: (fn: () => void, ms: number) => ReturnType<typeof setInterval> = setInterval,
  clearIntervalFn: (id: ReturnType<typeof setInterval>) => void = clearInterval,
): LivetailWatcher {
  let policy: FlushPolicy = 'normal';

  const id = setIntervalFn(() => {
    try {
      const until = livetailUntil();
      if (until !== null && until > now()) {
        policy = 'aggressive';
      } else {
        policy = 'normal';
      }
    } catch {
      // never crash the polling loop
    }
  }, 1000);

  return {
    stop() {
      clearIntervalFn(id);
    },
    policy() {
      return policy;
    },
  };
}
