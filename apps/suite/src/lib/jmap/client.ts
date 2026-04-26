/**
 * JMAP client substrate per docs/architecture/02-jmap-client.md.
 *
 * One client per session. Bootstraps via `GET /.well-known/jmap` to fetch
 * the session descriptor; thereafter `request()` POSTs method-call batches
 * to the descriptor's `apiUrl`.
 *
 * Auth is via the suite session cookie (resolved Q1). Every fetch sets
 * `credentials: 'include'` so the cookie attaches automatically; tabard
 * never reads or stores any auth token.
 */

import type {
  Invocation,
  MethodCallRequest,
  MethodCallResponse,
  SessionResource,
  ResultReference,
} from './types';
import {
  JmapMethodError,
  JmapTransportError,
  UnauthenticatedError,
} from './errors';

export class JmapClient {
  #session: SessionResource | null = null;
  #pinnedCapabilities: ReadonlySet<string> = new Set();

  /** The session descriptor; null until bootstrap() resolves. */
  get session(): SessionResource | null {
    return this.#session;
  }

  /** True if the server advertises the given capability URI. */
  hasCapability(name: string): boolean {
    return this.#pinnedCapabilities.has(name);
  }

  /**
   * Fetch `/.well-known/jmap` and pin the session for subsequent requests.
   */
  async bootstrap(): Promise<SessionResource> {
    const res = await fetch('/.well-known/jmap', {
      credentials: 'include',
      headers: { Accept: 'application/json' },
    });
    if (res.status === 401) {
      throw new UnauthenticatedError();
    }
    if (!res.ok) {
      throw new JmapTransportError(
        `Bootstrap failed: HTTP ${res.status}`,
        res.status,
      );
    }
    const session = (await res.json()) as SessionResource;
    this.#session = session;
    this.#pinnedCapabilities = new Set(Object.keys(session.capabilities));
    return session;
  }

  /**
   * Fire a method-call batch. Caller passes the using-list of capabilities
   * the methods require; the client posts and returns the parsed responses.
   *
   * Per-call method errors (`["error", ...]` invocations) are surfaced as
   * an array of resolved responses; the caller decides how strictly to
   * react. Use `request.strict()` for throw-on-any-error semantics.
   */
  async request(req: MethodCallRequest): Promise<MethodCallResponse> {
    if (!this.#session) {
      throw new JmapTransportError('JMAP client not bootstrapped', undefined);
    }
    const res = await fetch(this.#session.apiUrl, {
      method: 'POST',
      credentials: 'include',
      headers: {
        'Content-Type': 'application/json',
        Accept: 'application/json',
      },
      body: JSON.stringify(req),
    });
    if (res.status === 401) {
      throw new UnauthenticatedError();
    }
    if (!res.ok) {
      throw new JmapTransportError(
        `Request failed: HTTP ${res.status}`,
        res.status,
      );
    }
    return (await res.json()) as MethodCallResponse;
  }

  /**
   * Build a method-call batch with auto-assigned call IDs and back-reference
   * helpers. Each `b.call(...)` returns a small handle whose `.ref(path)`
   * produces the JMAP `ResultReference` pointing at that call's result.
   *
   * Example:
   *   const { responses } = await client.batch(b => {
   *     const q = b.call('Email/query', { ... }, [Capability.Mail]);
   *     b.call('Email/get', {
   *       accountId: '...',
   *       '#ids': q.ref('/ids'),
   *       properties: ['id', 'subject', 'from', 'preview'],
   *     }, [Capability.Mail]);
   *   });
   */
  async batch(
    builder: (b: BatchBuilder) => void,
  ): Promise<{ responses: Invocation[]; sessionState: string }> {
    const b = new BatchBuilder();
    builder(b);
    const using = Array.from(b.usingSet());
    const result = await this.request({
      using,
      methodCalls: b.calls(),
    });
    return { responses: result.methodResponses, sessionState: result.sessionState };
  }
}

/**
 * Throw on the first method-level error in the batch's responses. Useful
 * when the caller has no per-call recovery logic.
 */
export function strict(responses: Invocation[]): Invocation[] {
  for (const inv of responses) {
    if (inv[0] === 'error') {
      const [, args, callId] = inv as Invocation<{ type: string; description?: string }>;
      throw new JmapMethodError(callId, args);
    }
  }
  return responses;
}

class CallHandle {
  constructor(
    readonly id: string,
    readonly name: string,
  ) {}

  /** Build a ResultReference (RFC 8620 §3.7) pointing at this call's result. */
  ref(path: string): ResultReference {
    return { resultOf: this.id, name: this.name, path };
  }
}

class BatchBuilder {
  #counter = 0;
  #calls: Invocation[] = [];
  #using = new Set<string>();

  call<T>(name: string, args: T, using: readonly string[] = []): CallHandle {
    const id = `c${this.#counter++}`;
    this.#calls.push([name, args as unknown, id]);
    for (const u of using) this.#using.add(u);
    return new CallHandle(id, name);
  }

  calls(): Invocation[] {
    return this.#calls;
  }

  usingSet(): ReadonlySet<string> {
    return this.#using;
  }
}

/** Module-level singleton — there's one JMAP session per browser session. */
export const jmap = new JmapClient();
