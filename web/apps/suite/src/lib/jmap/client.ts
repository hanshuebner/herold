/**
 * JMAP client substrate per docs/architecture/02-jmap-client.md.
 *
 * One client per session. Bootstraps via `GET /.well-known/jmap` to fetch
 * the session descriptor; thereafter `request()` POSTs method-call batches
 * to the descriptor's `apiUrl`.
 *
 * Auth is via the suite session cookie (resolved Q1). Every fetch sets
 * `credentials: 'include'` so the cookie attaches automatically; the
 * suite never reads or stores any auth token.
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
   * Upload a blob via POST to the session uploadUrl (RFC 8620 §6.1).
   * Returns the parsed `{accountId, blobId, type, size}` response on
   * success. The body is streamed as-is; the caller picks the right
   * Content-Type (defaults to application/octet-stream).
   */
  async uploadBlob(args: {
    accountId: string;
    body: Blob;
    type?: string;
  }): Promise<{ accountId: string; blobId: string; type: string; size: number }> {
    const session = this.#session;
    if (!session) {
      throw new JmapTransportError('JMAP client not bootstrapped', undefined);
    }
    const url = session.uploadUrl.replace(
      '{accountId}',
      encodeURIComponent(args.accountId),
    );
    const res = await fetch(url, {
      method: 'POST',
      credentials: 'include',
      headers: {
        'Content-Type': args.type || args.body.type || 'application/octet-stream',
        Accept: 'application/json',
      },
      body: args.body,
    });
    if (res.status === 401) throw new UnauthenticatedError();
    if (!res.ok) {
      let detail = `HTTP ${res.status}`;
      try {
        const body = (await res.json()) as { type?: string; description?: string };
        if (body.description) detail = body.description;
        else if (body.type) detail = body.type;
      } catch {
        // body wasn't JSON; keep the HTTP status
      }
      throw new JmapTransportError(`Upload failed: ${detail}`, res.status);
    }
    return (await res.json()) as {
      accountId: string;
      blobId: string;
      type: string;
      size: number;
    };
  }

  /** Maximum bytes per upload from the core capability, or null when unknown. */
  get maxUploadSize(): number | null {
    const core = this.#session?.capabilities['urn:ietf:params:jmap:core'] as
      | { maxSizeUpload?: number }
      | undefined;
    return core?.maxSizeUpload ?? null;
  }

  /**
   * Resolve a JMAP downloadUrl template (RFC 8620 §6.2) for a blob.
   * The session.downloadUrl carries `{accountId}`, `{blobId}`, `{type}`,
   * and `{name}` placeholders; this helper substitutes the obvious four
   * with URL-encoded values. Returns null when no session is bootstrapped.
   *
   * `disposition`: pass `'inline'` when the URL feeds an `<iframe>` or
   * `<embed>` (PDF preview, image lightbox) so the server emits
   * `Content-Disposition: inline` and the browser renders the resource
   * in-page. Default `'attachment'` keeps the download-link behaviour —
   * herold's blob endpoint forces `attachment` when the parameter is
   * absent or unrecognised.
   */
  downloadUrl(args: {
    accountId: string;
    blobId: string;
    type?: string;
    name?: string;
    disposition?: 'inline' | 'attachment';
  }): string | null {
    const session = this.#session;
    if (!session) return null;
    const base = session.downloadUrl
      .replace('{accountId}', encodeURIComponent(args.accountId))
      .replace('{blobId}', encodeURIComponent(args.blobId))
      .replace('{type}', encodeURIComponent(args.type ?? 'application/octet-stream'))
      .replace('{name}', encodeURIComponent(args.name ?? 'attachment'));
    if (args.disposition === 'inline') {
      const sep = base.includes('?') ? '&' : '?';
      return `${base}${sep}disposition=inline`;
    }
    return base;
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
