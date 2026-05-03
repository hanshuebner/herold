/**
 * Client-side symbolication adapter (REQ-OPS-212).
 *
 * Fetches the source map for a given build SHA, parses it with the
 * `source-map` library, and rewrites minified stack frames in place.
 * Symbolication is purely client-side; the server stores raw stacks.
 *
 * The admin viewer calls symbolicateStack() once per detail pane open.
 * Results are cached by build SHA for the page session so repeated opens
 * of the same build do not re-fetch.
 *
 * If the map cannot be fetched (404, network error) the function throws
 * a SymbolicateError with a human-readable message so the caller can
 * surface a small inline fallback message.
 *
 * Map files are publicly fetchable from /assets/*.map because herold is
 * open-source (REQ-OPS-212 rationale).
 */

import { SourceMapConsumer } from 'source-map';

export class SymbolicateError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'SymbolicateError';
  }
}

/**
 * In-memory cache keyed by map URL string.
 * A SourceMapConsumer is expensive to construct; we keep one per map.
 */
const consumerCache = new Map<string, SourceMapConsumer>();

/** Regex that matches a minified V8/SpiderMonkey stack frame line. */
const FRAME_RE =
  /^\s+at .+?\(?(https?:\/\/[^)]+\/assets\/[^)]+\.js):(\d+):(\d+)\)?/;

/**
 * Determine the best source-map URL for a given build SHA.
 *
 * In development Vite writes /admin/assets/index-<hash>.js.map; in
 * production build the SHA is embedded in the filename. Since we cannot
 * reliably reconstruct the exact chunk filename from the SHA alone, we
 * first attempt to read the `//# sourceMappingURL=` comment from the
 * first 2 KiB of the matching JS bundle listed in the inline comment.
 *
 * This implementation uses a simpler heuristic: fetch
 * /admin/assets/index.js.map as the primary map and fall back to a
 * build-sha-prefixed path if the index map's `file` attribute names a
 * different SHA.
 *
 * In practice the admin SPA is a single chunk; there is one map per build.
 */
function mapUrlForBuildSha(buildSha: string): string {
  // The Vite build produces /admin/assets/index-<hash>.js.map.
  // We cannot know the hash, but the <meta name="herold-build"> SHA is
  // unique per build. The server may serve a canonical alias at
  // /admin/assets/index.js.map in future; for now we try to fetch
  // the map for the currently loaded bundle by looking at the scripts
  // already present in the page.
  const scripts = Array.from(document.querySelectorAll<HTMLScriptElement>('script[src]'));
  for (const s of scripts) {
    const src = s.src;
    if (src.includes('/assets/') && src.endsWith('.js')) {
      return src + '.map';
    }
  }
  // Fallback: try a deterministic path the build pipeline could provide.
  return `/admin/assets/${buildSha}.js.map`;
}

/**
 * Fetch and cache the SourceMapConsumer for a build SHA.
 * Throws SymbolicateError on any fetch/parse failure.
 */
async function getConsumer(buildSha: string): Promise<SourceMapConsumer> {
  const mapUrl = mapUrlForBuildSha(buildSha);
  const cached = consumerCache.get(mapUrl);
  if (cached !== undefined) return cached;

  let response: Response;
  try {
    response = await fetch(mapUrl, { credentials: 'same-origin' });
  } catch (err) {
    throw new SymbolicateError(
      `Could not fetch source map (network error): ${err instanceof Error ? err.message : String(err)}`,
    );
  }
  if (!response.ok) {
    throw new SymbolicateError(
      `Could not fetch source map: HTTP ${response.status} from ${mapUrl}`,
    );
  }

  let rawMap: string;
  try {
    rawMap = await response.text();
  } catch (err) {
    throw new SymbolicateError(
      `Could not read source map body: ${err instanceof Error ? err.message : String(err)}`,
    );
  }

  let consumer: SourceMapConsumer;
  try {
    // SourceMapConsumer constructor accepts a raw JSON string or object.
    consumer = await new SourceMapConsumer(rawMap);
  } catch (err) {
    throw new SymbolicateError(
      `Could not parse source map: ${err instanceof Error ? err.message : String(err)}`,
    );
  }

  consumerCache.set(mapUrl, consumer);
  return consumer;
}

/**
 * Rewrite minified frames in a raw V8/SpiderMonkey stack trace string.
 * Returns the symbolicated stack string. Lines that do not match the
 * frame pattern are kept verbatim.
 *
 * Throws SymbolicateError if the source map cannot be fetched or parsed.
 */
export async function symbolicateStack(
  rawStack: string,
  buildSha: string,
): Promise<string> {
  const consumer = await getConsumer(buildSha);

  const lines = rawStack.split('\n');
  const out: string[] = [];

  for (const line of lines) {
    const m = FRAME_RE.exec(line);
    if (!m) {
      out.push(line);
      continue;
    }
    const lineNum = parseInt(m[2] ?? '0', 10);
    const colNum = parseInt(m[3] ?? '0', 10);

    const pos = consumer.originalPositionFor({ line: lineNum, column: colNum });
    if (pos.source !== null && pos.line !== null) {
      const name = pos.name ?? '<anonymous>';
      const src = pos.source.replace(/^webpack:\/\/\//, '');
      const rewritten = `    at ${name} (${src}:${pos.line}:${pos.column ?? 0})`;
      out.push(rewritten);
    } else {
      out.push(line);
    }
  }

  return out.join('\n');
}

/**
 * Clear the consumer cache. Used in tests to reset state between cases.
 */
export function _resetConsumerCache(): void {
  consumerCache.clear();
}
