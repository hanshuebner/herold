/**
 * Suite clientlog integration.
 *
 * Holds the singleton Clientlog instance and the reactive predicates that
 * the install() call requires. The predicates read from auth.svelte and the
 * JMAP session descriptor so they stay up to date as auth state changes.
 *
 * install() is called before mount() in main.ts so a crash during JMAP
 * setup is captured (REQ-CLOG-01).
 *
 * REQ-CLOG-01, REQ-CLOG-05, REQ-CLOG-06, REQ-CLOG-12, REQ-CLOG-20.
 */

import { install } from '@herold/clientlog';
import type { Clientlog } from '@herold/clientlog';
import { auth } from '../auth/auth.svelte';

/** Capability URN for the herold client-log session extension. */
const CAP_CLIENTLOG = 'urn:netzhansa:params:jmap:clientlog';

/** Shape of the session descriptor's clientlog capability. */
interface ClientlogCapability {
  telemetry_enabled?: boolean;
  livetail_until?: string | null;
}

/**
 * Returns true once the first JMAP session descriptor has been received.
 * REQ-CLOG-07: the wrapper uses the anonymous endpoint until this is true.
 */
function isAuthenticated(): boolean {
  return auth.status === 'ready' && auth.session !== null;
}

/**
 * Returns the livetail expiry as a ms epoch, or null when absent / expired.
 * REQ-CLOG-05: the wrapper observes this every ~1 s to flip flush policy.
 */
function livetailUntil(): number | null {
  const cap = auth.session?.capabilities[CAP_CLIENTLOG] as
    | ClientlogCapability
    | undefined;
  if (!cap?.livetail_until) return null;
  const ms = Date.parse(cap.livetail_until);
  if (Number.isNaN(ms)) return null;
  return ms;
}

/**
 * Returns the per-user telemetry opt-in flag from the JMAP session.
 * REQ-CLOG-06: when false, kind=log and kind=vital events are suppressed.
 * Errors always pass through regardless of this value.
 */
function telemetryEnabled(): boolean {
  const cap = auth.session?.capabilities[CAP_CLIENTLOG] as
    | ClientlogCapability
    | undefined;
  // Fall back to true (default) when the capability is absent (pre-auth).
  return cap?.telemetry_enabled ?? true;
}

/**
 * Read the build SHA from <meta name="herold-build">.
 * Returns empty string when the tag is absent (dev mode).
 */
function readBuildSha(): string {
  try {
    return (
      document
        .querySelector<HTMLMetaElement>('meta[name="herold-build"]')
        ?.getAttribute('content') ?? ''
    );
  } catch {
    return '';
  }
}

/**
 * The installed Clientlog singleton. Assigned once in initClientlog()
 * and exported so error boundaries can call logFatal().
 *
 * Always non-null after initClientlog(); before that call the NOOP_CLIENTLOG
 * stub is active (assign as a let and initialise lazily is the cleaner
 * option, but we also want a synchronous import in main.ts before mount()).
 */
let _instance: Clientlog | null = null;

/**
 * Initialise the clientlog wrapper.
 *
 * Must be called exactly once, very early in main.ts — before keyboard.init()
 * and before mount(App, ...). Returns the Clientlog instance (or a no-op stub
 * if the bootstrap descriptor says enabled=false — REQ-CLOG-12).
 */
export function initClientlog(): Clientlog {
  _instance = install({
    app: 'suite',
    buildSha: readBuildSha(),
    endpoints: {
      authenticated: '/api/v1/clientlog',
      anonymous: '/api/v1/clientlog/public',
    },
    isAuthenticated,
    livetailUntil,
    telemetryEnabled,
  });
  return _instance;
}

/**
 * Returns the active Clientlog instance.
 * Callers that run after main.ts (e.g. error boundary components) use this
 * rather than importing the module-level private.
 */
export function getClientlog(): Clientlog {
  if (_instance !== null) return _instance;
  // Fallback: return a no-op stub so callers don't have to null-check.
  return { logFatal: () => Promise.resolve(), shutdown: () => undefined };
}

/**
 * Exported for vitest tests that need to reset singleton state.
 * Never called from production code.
 */
export const _internals_forTest = {
  isAuthenticated,
  livetailUntil,
  telemetryEnabled,
  resetInstance: () => {
    _instance = null;
  },
};
