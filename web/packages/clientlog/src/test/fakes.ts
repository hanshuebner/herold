/**
 * Deterministic fakes for clientlog unit tests (REQ-CLOG-14).
 *
 * installFakeClock()   -- replaces Date.now, performance.now, setTimeout,
 *                         clearTimeout, setInterval, clearInterval.
 * installFakeFetch()   -- replaces globalThis.fetch.
 * installFakeBeacon()  -- replaces navigator.sendBeacon.
 * installFakeUuid()    -- replaces crypto.randomUUID with a counter.
 *
 * All fakes return uninstall() functions. Tests should call uninstall in
 * afterEach to avoid cross-test pollution.
 *
 * No real timers, no real network, no real Date in any test that uses these
 * fakes. Flush is driven by clock.advance() or instance.flushNow() (flushNow
 * is exposed in tests via the Flusher directly; clock.advance triggers timers).
 */

export interface FakeClock {
  /** Current virtual time in ms. */
  now: number;
  /** Advance virtual time by delta ms, firing any due timers. */
  advance(deltaMs: number): void;
  /** Uninstall the fake and restore the originals. */
  uninstall(): void;
}

interface ScheduledTimer {
  id: number;
  dueAt: number;
  interval: number | null; // non-null for setInterval
  fn: () => void;
  cancelled: boolean;
}

export function installFakeClock(startMs = 1_700_000_000_000): FakeClock {
  let virtualNow = startMs;
  let nextId = 1;
  const timers: ScheduledTimer[] = [];

  const origDateNow = Date.now;
  const origPerfNow = performance.now.bind(performance);
  const origSetTimeout = globalThis.setTimeout;
  const origClearTimeout = globalThis.clearTimeout;
  const origSetInterval = globalThis.setInterval;
  const origClearInterval = globalThis.clearInterval;

  Date.now = () => virtualNow;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (performance as any).now = () => virtualNow - startMs;

  function fakeSetTimeout(fn: () => void, ms: number): number {
    const id = nextId++;
    timers.push({ id, dueAt: virtualNow + (ms ?? 0), interval: null, fn, cancelled: false });
    return id;
  }

  function fakeClearTimeout(id: number): void {
    const t = timers.find((x) => x.id === id);
    if (t) t.cancelled = true;
  }

  function fakeSetInterval(fn: () => void, ms: number): number {
    const id = nextId++;
    timers.push({ id, dueAt: virtualNow + ms, interval: ms, fn, cancelled: false });
    return id;
  }

  function fakeClearInterval(id: number): void {
    const t = timers.find((x) => x.id === id);
    if (t) t.cancelled = true;
  }

  // Assign to globals. We cast to any once to avoid TS complaints about
  // the overloaded signatures.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (globalThis as any).setTimeout = fakeSetTimeout;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (globalThis as any).clearTimeout = fakeClearTimeout;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (globalThis as any).setInterval = fakeSetInterval;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (globalThis as any).clearInterval = fakeClearInterval;

  const clock: FakeClock = {
    get now() {
      return virtualNow;
    },
    advance(deltaMs: number) {
      const target = virtualNow + deltaMs;
      // Fire all due timers in chronological order; allow them to schedule
      // new timers that may also be due within this advance window.
      let safety = 10000;
      while (safety-- > 0) {
        // Find the earliest due, non-cancelled timer within [virtualNow, target].
        const pending = timers
          .filter((t) => !t.cancelled && t.dueAt <= target)
          .sort((a, b) => a.dueAt - b.dueAt);
        if (pending.length === 0) break;
        const t = pending[0]!;
        virtualNow = t.dueAt;
        if (t.interval !== null) {
          // Reschedule interval.
          t.dueAt = virtualNow + t.interval;
        } else {
          t.cancelled = true;
        }
        t.fn();
      }
      virtualNow = target;
    },
    uninstall() {
      Date.now = origDateNow;
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (performance as any).now = origPerfNow;
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (globalThis as any).setTimeout = origSetTimeout;
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (globalThis as any).clearTimeout = origClearTimeout;
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (globalThis as any).setInterval = origSetInterval;
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (globalThis as any).clearInterval = origClearInterval;
    },
  };

  return clock;
}

export interface FetchCall {
  url: string;
  method: string;
  body: string;
  headers: Record<string, string>;
}

export interface FakeFetch {
  calls: FetchCall[];
  /** Set the response status for subsequent calls (default 200). */
  setStatus(status: number): void;
  uninstall(): void;
}

export function installFakeFetch(defaultStatus = 200): FakeFetch {
  const calls: FetchCall[] = [];
  let status = defaultStatus;
  const origFetch = globalThis.fetch;

  globalThis.fetch = async (
    input: RequestInfo | URL,
    init?: RequestInit,
  ): Promise<Response> => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url;
    const method = init?.method ?? 'GET';
    const body =
      init?.body instanceof Blob
        ? await init.body.text()
        : typeof init?.body === 'string'
          ? init.body
          : '';
    const headers: Record<string, string> = {};
    if (init?.headers) {
      if (init.headers instanceof Headers) {
        init.headers.forEach((v, k) => {
          headers[k.toLowerCase()] = v;
        });
      } else {
        const h = new Headers(init.headers);
        h.forEach((v, k) => {
          headers[k.toLowerCase()] = v;
        });
      }
    }
    calls.push({ url, method, body, headers });
    return new Response(null, { status });
  };

  return {
    calls,
    setStatus(s: number) {
      status = s;
    },
    uninstall() {
      globalThis.fetch = origFetch;
    },
  };
}

export interface BeaconCall {
  url: string;
  body: string;
}

export interface FakeBeacon {
  calls: BeaconCall[];
  /** When false, sendBeacon will return false, triggering retry logic. */
  returnValue: boolean;
  uninstall(): void;
}

export function installFakeBeacon(): FakeBeacon {
  const calls: BeaconCall[] = [];
  let returnValue = true;

  const origSendBeacon = navigator.sendBeacon.bind(navigator);

  navigator.sendBeacon = (url: string, data?: BodyInit | null): boolean => {
    let body = '';
    if (data instanceof Blob) {
      // Synchronous read is not available for Blob in the test environment;
      // we store the raw object and convert lazily. For simplicity we read
      // a pre-serialised string if available; tests can check calls[n].body.
      // Use a synchronous approach: store a placeholder and let the test read it.
      // Actually, in happy-dom / node, we can use the fake reader trick:
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      body = (data as any)._text ?? '';
    } else if (typeof data === 'string') {
      body = data;
    }
    calls.push({ url, body });
    return returnValue;
  };

  // For Blob bodies, happy-dom's Blob has no synchronous text() so we need
  // a workaround. We replace Blob to record its text at construction time.
  const OrigBlob = globalThis.Blob;
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (globalThis as any).Blob = class FakeBlob extends OrigBlob {
    _text: string;
    constructor(parts?: BlobPart[], options?: BlobPropertyBag) {
      super(parts, options);
      this._text =
        parts
          ?.map((p) => (typeof p === 'string' ? p : ''))
          .join('') ?? '';
    }
  };

  return {
    calls,
    get returnValue() {
      return returnValue;
    },
    set returnValue(v: boolean) {
      returnValue = v;
    },
    uninstall() {
      navigator.sendBeacon = origSendBeacon;
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (globalThis as any).Blob = OrigBlob;
    },
  };
}

export interface FakeUuid {
  counter: number;
  uninstall(): void;
}

export function installFakeUuid(): FakeUuid {
  let counter = 0;
  const origRandomUUID = crypto.randomUUID.bind(crypto);

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (crypto as any).randomUUID = (): string => {
    const n = counter++;
    // Return a deterministic UUID-shaped string.
    const hex = n.toString(16).padStart(8, '0');
    return `${hex}-0000-4000-8000-000000000000`;
  };

  return {
    get counter() {
      return counter;
    },
    uninstall() {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      (crypto as any).randomUUID = origRandomUUID;
    },
  };
}
