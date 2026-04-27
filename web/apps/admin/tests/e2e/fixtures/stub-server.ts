/**
 * Lightweight HTTP stub server for Playwright e2e tests.
 *
 * Each test file creates one StubServer per worker via test.use(stubServerFixture).
 * The server listens on a random ephemeral port, lets each test register handlers
 * for specific method+path pairs, and tracks the request log so assertions can
 * verify which endpoints were called.
 *
 * The stub implements the cookie + CSRF dance the admin SPA expects:
 *   - POST /api/v1/auth/login: on success, sets herold_admin_csrf (non-HttpOnly)
 *     and herold_admin_session (HttpOnly) cookies in the response. The CSRF token
 *     value is "test-csrf-token".
 *   - Subsequent mutating calls: validates X-CSRF-Token == "test-csrf-token" and
 *     returns 403 if missing/wrong (the SPA reads the csrf cookie from document.cookie).
 *
 * Usage in a spec:
 *   const { stub, baseURL } = use(stubServerFixture);
 *   stub.on('GET', '/api/v1/principals', (req, res) => { ... });
 */

import * as http from 'node:http';
import type { IncomingMessage, ServerResponse } from 'node:http';

export const CSRF_TOKEN = 'test-csrf-token';
export const SESSION_COOKIE = 'herold_admin_session=test-session; Path=/; HttpOnly; SameSite=Lax';
export const CSRF_COOKIE = `herold_admin_csrf=${CSRF_TOKEN}; Path=/; SameSite=Lax`;

export interface RecordedRequest {
  method: string;
  path: string;
  headers: Record<string, string | string[] | undefined>;
  body: string;
}

type Handler = (req: IncomingMessage, res: ServerResponse, body: string) => void;

export class StubServer {
  private server: http.Server;
  private handlers = new Map<string, Handler>();
  public requests: RecordedRequest[] = [];
  private port = 0;

  constructor() {
    this.server = http.createServer((req, res) => {
      void this.handleRequest(req, res);
    });
  }

  private async handleRequest(req: IncomingMessage, res: ServerResponse): Promise<void> {
    const method = req.method ?? 'GET';
    const path = req.url ?? '/';
    // Strip query string for handler lookup key (handlers match on path prefix).
    const pathKey = path.split('?')[0];

    let body = '';
    for await (const chunk of req) {
      body += String(chunk);
    }

    this.requests.push({
      method,
      path,
      headers: req.headers as Record<string, string | string[] | undefined>,
      body,
    });

    const key = `${method} ${pathKey}`;
    const wildcardKey = `${method} *`;

    const handler = this.handlers.get(key) ?? this.handlers.get(wildcardKey);
    if (handler) {
      handler(req, res, body);
      return;
    }

    // Default 404 for unregistered routes.
    res.writeHead(404, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ error: `no stub handler for ${key}` }));
  }

  /** Register a handler for a specific HTTP method + exact path (no query). */
  on(method: string, path: string, handler: Handler): void {
    this.handlers.set(`${method} ${path}`, handler);
  }

  /** Convenience: respond with JSON for GET requests. */
  get(path: string, data: unknown, status = 200): void {
    this.on('GET', path, (_req, res) => {
      res.writeHead(status, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(data));
    });
  }

  /** Convenience: respond with JSON for POST requests after validating CSRF. */
  post(path: string, data: unknown, status = 200, skipCsrf = false): void {
    this.on('POST', path, (req, res) => {
      if (!skipCsrf && !this.checkCsrf(req, res)) return;
      res.writeHead(status, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(data));
    });
  }

  /** Convenience: respond with JSON for PATCH requests after validating CSRF. */
  patch(path: string, data: unknown, status = 200): void {
    this.on('PATCH', path, (req, res) => {
      if (!this.checkCsrf(req, res)) return;
      res.writeHead(status, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(data));
    });
  }

  /** Convenience: respond for DELETE requests after validating CSRF. */
  delete(path: string, data: unknown = null, status = 204): void {
    this.on('DELETE', path, (req, res) => {
      if (!this.checkCsrf(req, res)) return;
      if (status === 204) {
        res.writeHead(204);
        res.end();
      } else {
        res.writeHead(status, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify(data));
      }
    });
  }

  private checkCsrf(req: IncomingMessage, res: ServerResponse): boolean {
    const token = req.headers['x-csrf-token'];
    if (token !== CSRF_TOKEN) {
      res.writeHead(403, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: 'csrf token missing or invalid' }));
      return false;
    }
    return true;
  }

  /**
   * Install the login endpoint.
   *
   * On success (email contains "admin@") sets session + CSRF cookies.
   * Returns { step_up_required: true } with 401 if password === "totp-required".
   * Returns 401 with error message for any other credential.
   */
  installLoginEndpoint(): void {
    this.on('POST', '/api/v1/auth/login', (_req, res, body) => {
      let parsed: { email?: string; password?: string; totp_code?: string } = {};
      try {
        parsed = JSON.parse(body) as typeof parsed;
      } catch {
        // ignore parse errors
      }

      const { email = '', password = '', totp_code } = parsed;

      // Happy path: correct credentials (email contains "admin@", password == "correct")
      if (email.includes('admin@') && password === 'correct') {
        res.writeHead(200, {
          'Content-Type': 'application/json',
          'Set-Cookie': [SESSION_COOKIE, CSRF_COOKIE],
        });
        res.end(
          JSON.stringify({
            principal_id: '1',
            scopes: ['admin'],
          }),
        );
        return;
      }

      // TOTP step-up path: password "totp-required" triggers step-up on first attempt
      if (email.includes('admin@') && password === 'totp-required' && !totp_code) {
        res.writeHead(401, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ step_up_required: true }));
        return;
      }

      // TOTP confirmed: second submit with any 6-digit code succeeds
      if (email.includes('admin@') && password === 'totp-required' && totp_code) {
        res.writeHead(200, {
          'Content-Type': 'application/json',
          'Set-Cookie': [SESSION_COOKIE, CSRF_COOKIE],
        });
        res.end(
          JSON.stringify({
            principal_id: '1',
            scopes: ['admin'],
          }),
        );
        return;
      }

      // Wrong credentials
      res.writeHead(401, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ message: 'Invalid email or password.' }));
    });
  }

  /** Install GET /api/v1/server/status (used by auth.bootstrap()). */
  installStatusEndpoint(authenticated = true): void {
    this.on('GET', '/api/v1/server/status', (_req, res) => {
      if (!authenticated) {
        res.writeHead(401, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: 'not authenticated' }));
        return;
      }
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          principal_id: '1',
          email: 'admin@example.com',
          scopes: ['admin'],
        }),
      );
    });
  }

  /** Install POST /api/v1/auth/logout. */
  installLogoutEndpoint(): void {
    this.on('POST', '/api/v1/auth/logout', (_req, res) => {
      res.writeHead(204);
      res.end();
    });
  }

  /** Start the server and return the port it is listening on. */
  async start(): Promise<number> {
    return new Promise((resolve, reject) => {
      this.server.listen(0, '127.0.0.1', () => {
        const addr = this.server.address();
        if (!addr || typeof addr === 'string') {
          reject(new Error('unexpected address type'));
          return;
        }
        this.port = addr.port;
        resolve(this.port);
      });
    });
  }

  /** Stop the server. Called from afterAll in each spec. */
  async stop(): Promise<void> {
    return new Promise((resolve, reject) => {
      this.server.close((err) => {
        if (err) reject(err);
        else resolve();
      });
    });
  }

  /** Clear the recorded request log between tests. */
  clearLog(): void {
    this.requests = [];
  }

  /** Filter the request log by method and path prefix. */
  called(method: string, pathPrefix: string): RecordedRequest[] {
    return this.requests.filter(
      (r) => r.method === method && r.path.startsWith(pathPrefix),
    );
  }

  getPort(): number {
    return this.port;
  }
}
