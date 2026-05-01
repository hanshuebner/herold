/**
 * Bootstrap descriptor reader.
 *
 * Two sources, in priority order:
 * 1. <meta name="herold-clientlog"> tag -- available from first paint.
 * 2. JMAP session descriptor capability (herold vendor URI) -- available
 *    post-auth; richer; overrides meta values.
 *
 * REQ-CLOG-12: if the tag is absent or enabled=false, the wrapper installs
 * nothing and emits nothing.
 */

export interface MetaDescriptor {
  enabled: boolean;
  batch_max_events: number;
  batch_max_age_ms: number;
  queue_cap: number;
  telemetry_enabled_default: boolean;
}

export interface SessionDescriptor {
  telemetry_enabled: boolean;
  livetail_until: number | null;
}

/** Resolved bootstrap config after merging meta and optional session desc. */
export interface BootstrapDescriptor {
  enabled: boolean;
  batch_max_events: number;
  batch_max_age_ms: number;
  queue_cap: number;
  telemetry_enabled_default: boolean;
}

const DEFAULTS: BootstrapDescriptor = {
  enabled: true,
  batch_max_events: 20,
  batch_max_age_ms: 5000,
  queue_cap: 200,
  telemetry_enabled_default: true,
};

/**
 * Reads the <meta name="herold-clientlog"> tag and returns the parsed
 * descriptor. Returns null when the tag is absent or the JSON is invalid.
 * Returns a descriptor with enabled=false when the tag says so.
 */
export function readMetaDescriptor(): BootstrapDescriptor | null {
  try {
    const el = document.querySelector<HTMLMetaElement>(
      'meta[name="herold-clientlog"]',
    );
    if (!el) return null;
    const raw = el.getAttribute('content');
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<MetaDescriptor>;
    return {
      enabled: parsed.enabled ?? DEFAULTS.enabled,
      batch_max_events: parsed.batch_max_events ?? DEFAULTS.batch_max_events,
      batch_max_age_ms: parsed.batch_max_age_ms ?? DEFAULTS.batch_max_age_ms,
      queue_cap: parsed.queue_cap ?? DEFAULTS.queue_cap,
      telemetry_enabled_default:
        parsed.telemetry_enabled_default ?? DEFAULTS.telemetry_enabled_default,
    };
  } catch {
    return null;
  }
}

/**
 * Returns the build SHA from <meta name="herold-build">, or empty string
 * if absent.
 */
export function readBuildSha(): string {
  try {
    const el = document.querySelector<HTMLMetaElement>(
      'meta[name="herold-build"]',
    );
    return el?.getAttribute('content') ?? '';
  } catch {
    return '';
  }
}
