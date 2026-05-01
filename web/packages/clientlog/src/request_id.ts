/**
 * RequestIdContext -- in-flight request-id thread-local.
 *
 * A lightweight async-context shim. JS is single-threaded, so we maintain
 * a stack of active request IDs. Each fetch wraps its body in run(); any
 * event captured while inside a run() call reads the current ID.
 *
 * REQ-CLOG-20: X-Request-Id UUID v7 generated client-side; the wrapper
 * records it on every event emitted while the request is in flight.
 *
 * UUID v7 is generated inline (Date.now() + crypto.getRandomValues) to
 * avoid a dependency. crypto.randomUUID() produces UUID v4; we need v7.
 */

const stack: string[] = [];

export const RequestIdContext = {
  /** Returns the innermost in-flight request id, if any. */
  current(): string | undefined {
    return stack[stack.length - 1];
  },

  /**
   * Runs fn() with id as the active request id, then pops it.
   * Works for both sync and async fn.
   */
  async run<T>(id: string, fn: () => Promise<T> | T): Promise<T> {
    stack.push(id);
    try {
      return await fn();
    } finally {
      // Pop this specific id (it may not be the last if run() calls are
      // nested -- in that case only pop the one we pushed).
      const idx = stack.lastIndexOf(id);
      if (idx !== -1) stack.splice(idx, 1);
    }
  },
};

/**
 * Generates a UUID v7 using Date.now() for the 48-bit ms timestamp and
 * crypto.getRandomValues for the remaining 74 random bits.
 * Format: xxxxxxxx-xxxx-7xxx-yxxx-xxxxxxxxxxxx
 *
 * REQ-CLOG-20: v7 preferred over v4 for time-ordered correlation.
 */
export function uuidv7(): string {
  const ms = Date.now();
  // 48-bit big-endian ms timestamp
  const hi = Math.floor(ms / 0x1_0000);
  const lo = ms & 0xffff;

  // 12 random bits (rand_a) + version nibble
  const rand = new Uint8Array(10);
  crypto.getRandomValues(rand);

  // version nibble = 7
  const timeHigh = ((rand[0]! & 0x0f) | 0x70).toString(16).padStart(2, '0');
  const randA = rand[1]!.toString(16).padStart(2, '0');

  // variant bits: 10xx (RFC 4122 variant)
  const variantByte = ((rand[2]! & 0x3f) | 0x80).toString(16).padStart(2, '0');

  const tail = Array.from(rand.slice(2), (b) => b.toString(16).padStart(2, '0')).join('');

  const p1 = hi.toString(16).padStart(8, '0');
  const p2 = lo.toString(16).padStart(4, '0');
  const p3 = timeHigh + randA;
  const p4 = variantByte + tail.slice(2, 4);
  const p5 = tail.slice(4, 16);

  return `${p1}-${p2}-${p3}-${p4}-${p5}`;
}

/**
 * Wraps a fetch function so every call is:
 * 1. Assigned a fresh UUID v7 as X-Request-Id.
 * 2. Executed inside RequestIdContext.run() so events captured during
 *    the fetch carry the same id.
 *
 * Usage:
 *   const jmapFetch = wrapFetch(globalThis.fetch.bind(globalThis));
 *
 * REQ-CLOG-20: applied by the host (Suite JMAP client, Admin REST client).
 */
export function wrapFetch(
  originalFetch: typeof globalThis.fetch,
): typeof globalThis.fetch {
  return (input: RequestInfo | URL, init?: RequestInit) => {
    const id = uuidv7();
    const headers = new Headers(init?.headers);
    headers.set('X-Request-Id', id);
    return RequestIdContext.run(id, () =>
      originalFetch(input, { ...init, headers }),
    );
  };
}
