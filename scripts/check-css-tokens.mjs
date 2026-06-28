#!/usr/bin/env node
/**
 * check-css-tokens.mjs -- fail if any var(--x) in web/apps/{suite,admin}/src
 * references a custom property not defined in the canonical design-system
 * tokens.css or declared via an inline style attribute in component markup.
 *
 * Exemptions:
 *   var(--x, fallback) forms: the fallback is intentional; these are skipped.
 *   Properties set via inline style="--x: ..." attributes in .svelte files
 *   or template-literal style assignments in .ts files: these are component-
 *   scoped and legitimately absent from the design-system file. Detection is
 *   generic -- we scan for --name: on any line that contains a style= context
 *   rather than keeping a hand-maintained allowlist, so new inline vars are
 *   automatically whitelisted without a script edit.
 *
 * Exit: 0 = clean, 1 = undefined references found.
 */

import { readFileSync, readdirSync, statSync } from 'fs';
import { join, resolve, extname } from 'path';
import { fileURLToPath } from 'url';

const __dirname = fileURLToPath(new URL('.', import.meta.url));
const repoRoot = resolve(__dirname, '..');

const TOKENS_CSS = join(
  repoRoot,
  'web/packages/design-system/src/tokens/tokens.css',
);

// Directories to scan for var(--x) references.
const SCAN_DIRS = [
  join(repoRoot, 'web/apps/suite/src'),
  join(repoRoot, 'web/apps/admin/src'),
];

// File extensions included in the scan.
const SCAN_EXTS = new Set(['.svelte', '.css', '.ts']);

/**
 * Extract every --name: custom-property definition from a CSS file.
 * This covers all property definitions regardless of selector or theme block.
 *
 * @param {string} content - CSS file content
 * @returns {Set<string>} set of token names with leading --
 */
function extractDefinedTokens(content) {
  const defined = new Set();
  const re = /--([a-zA-Z0-9-]+)\s*:/g;
  let m;
  while ((m = re.exec(content)) !== null) {
    defined.add('--' + m[1]);
  }
  return defined;
}

/**
 * Scan .svelte and .ts files for inline-style custom-property definitions:
 *   style="--x: ..."
 *   style={`--x: ${expr}`}
 *   style={someExpr ? `--x: ${v}` : undefined}
 *
 * Any line containing a style= attribute context (style= followed by a quote,
 * backtick, or brace) is inspected for --name: patterns.  This is a
 * line-oriented heuristic rather than a full parser; it is intentionally
 * conservative (only lines with style=) to avoid picking up CSS rule
 * definitions in <style> blocks.
 *
 * @param {Array<{path: string, content: string}>} files
 * @returns {Set<string>} set of inline-defined token names with leading --
 */
function extractInlineStyleTokens(files) {
  const inlineDefined = new Set();
  const styleLine = /\bstyle\s*=\s*["'`{]/;
  const propRe = /--([a-zA-Z0-9-]+)\s*:/g;

  for (const { path: filePath, content } of files) {
    const ext = extname(filePath);
    if (ext !== '.svelte' && ext !== '.ts') continue;

    for (const line of content.split('\n')) {
      if (!styleLine.test(line)) continue;
      propRe.lastIndex = 0;
      let m;
      while ((m = propRe.exec(line)) !== null) {
        inlineDefined.add('--' + m[1]);
      }
    }
  }
  return inlineDefined;
}

/**
 * Recursively collect all files under dirs with extensions in SCAN_EXTS.
 *
 * @param {string[]} dirs
 * @returns {Array<{path: string, content: string}>}
 */
function findFiles(dirs) {
  const files = [];
  function walk(dir) {
    for (const name of readdirSync(dir)) {
      const full = join(dir, name);
      const st = statSync(full);
      if (st.isDirectory()) {
        walk(full);
      } else if (SCAN_EXTS.has(extname(name))) {
        files.push({ path: full, content: readFileSync(full, 'utf8') });
      }
    }
  }
  for (const d of dirs) walk(d);
  return files;
}

/**
 * Extract every var(--x) reference that has NO fallback (i.e., the token name
 * is followed immediately by the closing paren).  var(--x, fallback) forms are
 * skipped because the fallback is intentional.
 *
 * @param {Array<{path: string, content: string}>} files
 * @returns {Array<{token: string, file: string, line: number}>}
 */
function extractUsedTokens(files) {
  const usages = [];
  const re = /var\((--[a-zA-Z0-9-]+)\)/g;

  for (const { path: filePath, content } of files) {
    const lines = content.split('\n');
    for (let i = 0; i < lines.length; i++) {
      re.lastIndex = 0;
      let m;
      while ((m = re.exec(lines[i])) !== null) {
        usages.push({ token: m[1], file: filePath, line: i + 1 });
      }
    }
  }
  return usages;
}

function main() {
  const tokensContent = readFileSync(TOKENS_CSS, 'utf8');
  const defined = extractDefinedTokens(tokensContent);

  const files = findFiles(SCAN_DIRS);
  const inlineDefined = extractInlineStyleTokens(files);

  const allDefined = new Set([...defined, ...inlineDefined]);

  const usages = extractUsedTokens(files);
  const offenders = usages.filter(u => !allDefined.has(u.token));

  if (offenders.length === 0) {
    process.stdout.write('check-css-tokens: all var(--x) references resolved\n');
    process.exit(0);
  }

  // Group by token name for a compact report.
  const byToken = new Map();
  for (const { token, file, line } of offenders) {
    if (!byToken.has(token)) byToken.set(token, []);
    byToken.get(token).push(
      '  ' + file.replace(repoRoot + '/', '') + ':' + line,
    );
  }

  let msg = 'check-css-tokens: undefined CSS token references (no fallback):\n';
  for (const [token, locs] of [...byToken.entries()].sort()) {
    msg += '\n' + token + '\n';
    for (const loc of locs) msg += loc + '\n';
  }
  msg +=
    '\nAdd the tokens to web/packages/design-system/src/tokens/tokens.css,\n';
  msg += 'or use var(--x, fallback) if the degraded fallback is intentional.\n';
  process.stderr.write(msg);
  process.exit(1);
}

main();
