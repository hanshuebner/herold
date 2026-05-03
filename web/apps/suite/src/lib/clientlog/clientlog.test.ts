/**
 * Unit tests for the Suite clientlog integration helpers.
 *
 * These tests exercise the predicate functions (isAuthenticated,
 * livetailUntil, telemetryEnabled) without touching the real install()
 * path, which depends on browser globals (DOM, sessionStorage, etc.).
 *
 * REQ-CLOG-05, REQ-CLOG-06, REQ-CLOG-12.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';

// ── Auth mock ─────────────────────────────────────────────────────────────

const mockAuth = {
  status: 'idle' as 'idle' | 'bootstrapping' | 'ready' | 'unauthenticated' | 'error',
  session: null as {
    capabilities: Record<string, unknown>;
    username: string;
  } | null,
};

vi.mock('../auth/auth.svelte', () => ({
  auth: mockAuth,
}));

// Re-import after mock is set up.
const { _internals_forTest } = await import('./clientlog.svelte');
const { isAuthenticated, livetailUntil, telemetryEnabled } = _internals_forTest;

describe('clientlog integration predicates', () => {
  beforeEach(() => {
    mockAuth.status = 'idle';
    mockAuth.session = null;
  });

  // ── isAuthenticated ────────────────────────────────────────────────────

  describe('isAuthenticated', () => {
    it('returns false when status is idle', () => {
      mockAuth.status = 'idle';
      mockAuth.session = null;
      expect(isAuthenticated()).toBe(false);
    });

    it('returns false when status is bootstrapping', () => {
      mockAuth.status = 'bootstrapping';
      mockAuth.session = null;
      expect(isAuthenticated()).toBe(false);
    });

    it('returns false when status is unauthenticated', () => {
      mockAuth.status = 'unauthenticated';
      mockAuth.session = null;
      expect(isAuthenticated()).toBe(false);
    });

    it('returns false when status is ready but session is null', () => {
      mockAuth.status = 'ready';
      mockAuth.session = null;
      expect(isAuthenticated()).toBe(false);
    });

    it('returns true when status is ready and session is present', () => {
      mockAuth.status = 'ready';
      mockAuth.session = { capabilities: {}, username: 'user@example.com' };
      expect(isAuthenticated()).toBe(true);
    });
  });

  // ── livetailUntil ─────────────────────────────────────────────────────

  describe('livetailUntil', () => {
    it('returns null when no session', () => {
      mockAuth.session = null;
      expect(livetailUntil()).toBeNull();
    });

    it('returns null when capability absent', () => {
      mockAuth.session = { capabilities: {}, username: 'u@e.com' };
      expect(livetailUntil()).toBeNull();
    });

    it('returns null when livetail_until is null', () => {
      mockAuth.session = {
        capabilities: {
          'urn:netzhansa:params:jmap:clientlog': { telemetry_enabled: true, livetail_until: null },
        },
        username: 'u@e.com',
      };
      expect(livetailUntil()).toBeNull();
    });

    it('returns null when livetail_until is absent', () => {
      mockAuth.session = {
        capabilities: {
          'urn:netzhansa:params:jmap:clientlog': { telemetry_enabled: true },
        },
        username: 'u@e.com',
      };
      expect(livetailUntil()).toBeNull();
    });

    it('returns null when livetail_until is not a valid date string', () => {
      mockAuth.session = {
        capabilities: {
          'urn:netzhansa:params:jmap:clientlog': { livetail_until: 'not-a-date' },
        },
        username: 'u@e.com',
      };
      expect(livetailUntil()).toBeNull();
    });

    it('returns ms epoch when livetail_until is a valid RFC 3339 string', () => {
      const future = new Date(Date.now() + 60_000).toISOString();
      mockAuth.session = {
        capabilities: {
          'urn:netzhansa:params:jmap:clientlog': { livetail_until: future },
        },
        username: 'u@e.com',
      };
      const result = livetailUntil();
      expect(result).not.toBeNull();
      expect(typeof result).toBe('number');
      expect(result).toBeGreaterThan(Date.now());
    });
  });

  // ── telemetryEnabled ──────────────────────────────────────────────────

  describe('telemetryEnabled', () => {
    it('returns true when no session (pre-auth default)', () => {
      mockAuth.session = null;
      expect(telemetryEnabled()).toBe(true);
    });

    it('returns true when capability absent', () => {
      mockAuth.session = { capabilities: {}, username: 'u@e.com' };
      expect(telemetryEnabled()).toBe(true);
    });

    it('returns false when telemetry_enabled is false', () => {
      mockAuth.session = {
        capabilities: {
          'urn:netzhansa:params:jmap:clientlog': { telemetry_enabled: false },
        },
        username: 'u@e.com',
      };
      expect(telemetryEnabled()).toBe(false);
    });

    it('returns true when telemetry_enabled is true', () => {
      mockAuth.session = {
        capabilities: {
          'urn:netzhansa:params:jmap:clientlog': { telemetry_enabled: true },
        },
        username: 'u@e.com',
      };
      expect(telemetryEnabled()).toBe(true);
    });

    it('returns true when telemetry_enabled is absent (fallback to default)', () => {
      mockAuth.session = {
        capabilities: {
          'urn:netzhansa:params:jmap:clientlog': {},
        },
        username: 'u@e.com',
      };
      expect(telemetryEnabled()).toBe(true);
    });
  });
});
