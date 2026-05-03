#!/usr/bin/env node
/**
 * scripts/bundle.mjs
 *
 * Bundles Markdoc .mdoc files into JSON (and optionally SSR HTML).
 *
 * Usage:
 *   node scripts/bundle.mjs \
 *     --manifest <path>       path to manifest.toml
 *     --content-root <dir>    root dir for .mdoc files
 *     --out-json <dir>        output dir for user.json / admin.json
 *     --out-ssr <dir>         output dir for SSR HTML (requires --ssr)
 *     [--ssr]                 also emit per-chapter HTML via svelte/server
 *
 * Exits non-zero on any validation error.
 */

import { readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs';
import { resolve, join, dirname, relative, extname } from 'node:path';
import { parseArgs } from 'node:util';
import { createRequire } from 'node:module';

// ---------------------------------------------------------------------------
// Argument parsing
// ---------------------------------------------------------------------------

const { values: argv } = parseArgs({
  args: process.argv.slice(2),
  options: {
    manifest: { type: 'string' },
    'content-root': { type: 'string' },
    'out-json': { type: 'string' },
    'out-ssr': { type: 'string' },
    ssr: { type: 'boolean', default: false },
  },
  strict: true,
});

if (!argv.manifest) fatal('--manifest is required');
if (!argv['content-root']) fatal('--content-root is required');
if (!argv['out-json']) fatal('--out-json is required');
if (argv.ssr && !argv['out-ssr']) fatal('--out-ssr is required when --ssr is set');

const manifestPath = resolve(/** @type {string} */ (argv.manifest));
const contentRoot = resolve(/** @type {string} */ (argv['content-root']));
const outJson = resolve(/** @type {string} */ (argv['out-json']));
const outSsr = argv['out-ssr'] ? resolve(argv['out-ssr']) : null;

// ---------------------------------------------------------------------------
// TOML parsing (tiny subset — only what manifest.toml needs)
// ---------------------------------------------------------------------------

/**
 * Minimal TOML parser for manifest.toml.
 *
 * Supports:
 *   [section] and [[array-of-tables]]
 *   key = "string"
 *   key = true/false
 *   # comments
 *
 * Does NOT support: inline tables, multi-line strings, dates, floats,
 * integers beyond ASCII digits.  That is sufficient for manifest.toml.
 *
 * @param {string} src
 * @returns {Record<string, unknown>}
 */
function parseToml(src) {
  const root = /** @type {Record<string, unknown>} */ ({});
  let current = root;
  let currentArrayKey = /** @type {string | null} */ (null);
  const lines = src.split('\n');

  for (let i = 0; i < lines.length; i++) {
    const raw = lines[i];
    if (raw === undefined) continue;
    const line = raw.replace(/#[^"']*$/, '').trim();
    if (line === '') continue;

    // [[array-of-tables]]
    const arrayMatch = line.match(/^\[\[([^\]]+)\]\]$/);
    if (arrayMatch) {
      const key = arrayMatch[1].trim();
      // Navigate to the right object.  Key may be dotted: "user.chapters"
      const parts = key.split('.');
      let obj = root;
      for (let j = 0; j < parts.length - 1; j++) {
        const p = parts[j];
        if (p === undefined) continue;
        if (!(p in obj)) obj[p] = {};
        obj = /** @type {Record<string, unknown>} */ (obj[p]);
      }
      const last = parts[parts.length - 1];
      if (last === undefined) continue;
      if (!Array.isArray(obj[last])) obj[last] = [];
      const arr = /** @type {Record<string, unknown>[]} */ (obj[last]);
      const newObj = /** @type {Record<string, unknown>} */ ({});
      arr.push(newObj);
      current = newObj;
      currentArrayKey = key;
      continue;
    }

    // [section]
    const sectionMatch = line.match(/^\[([^\]]+)\]$/);
    if (sectionMatch) {
      const key = sectionMatch[1].trim();
      currentArrayKey = null;
      const parts = key.split('.');
      let obj = root;
      for (const part of parts) {
        if (!(part in obj)) obj[part] = {};
        obj = /** @type {Record<string, unknown>} */ (obj[part]);
      }
      current = obj;
      continue;
    }

    // key = value
    const kvMatch = line.match(/^([\w.-]+)\s*=\s*(.+)$/);
    if (kvMatch) {
      const key = kvMatch[1].trim();
      const rawVal = kvMatch[2].trim();
      let value;
      if (rawVal === 'true') value = true;
      else if (rawVal === 'false') value = false;
      else if (/^"([^"]*)"$/.test(rawVal)) value = rawVal.slice(1, -1);
      else if (/^'([^']*)'$/.test(rawVal)) value = rawVal.slice(1, -1);
      else value = rawVal;
      current[key] = value;
      continue;
    }
  }

  return root;
}

// ---------------------------------------------------------------------------
// Markdoc imports
// ---------------------------------------------------------------------------

// We import Markdoc as CJS via createRequire because the bundler script is
// ESM but @markdoc/markdoc ships a CJS bundle.
const require = createRequire(import.meta.url);
const Markdoc = /** @type {typeof import('@markdoc/markdoc').default} */ (
  require('@markdoc/markdoc')
);

// Load our schema from the compiled TS (we use the JS-side schema directly).
// Because this script is run from Node (not Vite), we import the TS source
// via the schema re-exported as ESM.  The schema file is pure TypeScript with
// no Svelte dependencies, so we can import it directly in Node 22+ with
// --experimental-strip-types, or we fall back to reading the CJS output.
//
// Strategy: dynamically import from the schema.ts source if --input-type
// supports it, otherwise use a thin inline re-export.  We inline the schema
// definition here to avoid requiring tsx/ts-node at bundle time.

/** @type {Record<string, import('@markdoc/markdoc').Schema>} */
const tags = buildTagSchemas(Markdoc);

/** @param {typeof import('@markdoc/markdoc').default} M */
function buildTagSchemas(M) {
  const { Tag } = M;
  return {
    callout: {
      render: 'Callout',
      children: ['paragraph', 'fence', 'list'],
      attributes: {
        type: { type: String, default: 'info', matches: ['info', 'warning', 'caution'], errorLevel: 'error' },
        title: { type: String },
      },
      transform(node, config) {
        return new Tag('Callout', {
          type: node.attributes['type'],
          title: node.attributes['title'],
        }, node.transformChildren(config));
      },
    },
    'code-group': {
      render: 'CodeGroup',
      children: ['fence'],
      attributes: {},
      transform(node, config) {
        return new Tag('CodeGroup', {}, node.transformChildren(config));
      },
    },
    include: {
      render: 'IncludedCode',
      selfClosing: true,
      attributes: {
        file: { type: String, required: true, errorLevel: 'error' },
        lang: { type: String, default: 'text' },
        // `content` is injected by the bundler before validation; declare it
        // so Markdoc does not emit an "Invalid attribute" error.
        content: { type: String },
      },
      transform(node, _config) {
        return new Tag('IncludedCode', {
          file: node.attributes['file'],
          lang: node.attributes['lang'],
          content: node.attributes['content'],
        });
      },
    },
    req: {
      render: 'Req',
      selfClosing: true,
      attributes: {
        id: { type: String, required: true, errorLevel: 'error' },
      },
      transform(node, _config) {
        return new Tag('Req', { id: node.attributes['id'] });
      },
    },
    kbd: {
      render: 'Kbd',
      selfClosing: true,
      attributes: {
        keys: { type: String, required: true, errorLevel: 'error' },
      },
      transform(node, _config) {
        return new Tag('Kbd', { keys: node.attributes['keys'] });
      },
    },
  };
}

const allowedTagNames = new Set(Object.keys(tags));

/** @type {import('@markdoc/markdoc').Config} */
const markdocConfig = { tags };

// ---------------------------------------------------------------------------
// Validation helpers
// ---------------------------------------------------------------------------

/**
 * Validate an href attribute: must be one of:
 *   - same-document hash: #anchor
 *   - chapter + optional hash: {slug}  or  {slug}#anchor
 *   - external HTTPS: https://...
 *
 * @param {string} href
 * @param {Set<string>} slugs
 * @returns {string | null} error message or null if valid
 */
function validateHref(href, slugs) {
  if (href.startsWith('#')) return null;
  if (/^https:\/\//.test(href)) return null;
  // slug or slug#anchor
  const [slug, ...rest] = href.split('#');
  if (rest.length > 1) return `invalid href "${href}": multiple # characters`;
  if (slug && slugs.has(slug)) return null;
  return `invalid href "${href}": not a same-document hash, known slug, or https:// URL`;
}

/**
 * Walk the Markdoc AST and collect all error-level validation errors plus
 * unknown-tag usage.
 *
 * @param {import('@markdoc/markdoc').Node} ast
 * @param {string} filePath
 * @param {Set<string>} slugs
 * @returns {string[]}
 */
function collectErrors(ast, filePath, slugs) {
  const errors = [];

  // Markdoc structural validation
  const mdErrors = Markdoc.validate(ast, markdocConfig);
  for (const err of mdErrors) {
    if (err.error.level === 'error') {
      errors.push(`${filePath}: ${err.error.message} (line ${err.lines?.join('-') ?? '?'})`);
    }
  }

  // Walk for unknown tags and bad hrefs
  walkNode(ast, errors, filePath, slugs);

  return errors;
}

/**
 * @param {import('@markdoc/markdoc').Node} node
 * @param {string[]} errors
 * @param {string} filePath
 * @param {Set<string>} slugs
 */
function walkNode(node, errors, filePath, slugs) {
  if (node.type === 'tag') {
    const tagName = node.tag ?? '';
    if (tagName && !allowedTagNames.has(tagName)) {
      errors.push(`${filePath}: unknown tag "{% ${tagName} %}" — add it to src/markdoc/schema.ts or remove it`);
    }
  }
  if (node.type === 'link') {
    const href = node.attributes['href'];
    if (typeof href === 'string') {
      const err = validateHref(href, slugs);
      if (err) errors.push(`${filePath}: ${err}`);
    }
  }
  for (const child of node.children ?? []) {
    walkNode(child, errors, filePath, slugs);
  }
}

// ---------------------------------------------------------------------------
// Outline extraction
// ---------------------------------------------------------------------------

/**
 * Extract h2 and h3 headings from a RenderableTreeNode tree.
 *
 * Markdoc (without a custom heading schema) renders heading nodes directly
 * as h1..h6 tag names, not as a generic "heading" node with a level attribute.
 *
 * @param {import('@markdoc/markdoc').RenderableTreeNode} node
 * @returns {{ id: string, level: 2 | 3, text: string }[]}
 */
function extractOutline(node) {
  if (node === null || typeof node === 'string') return [];
  if (typeof node !== 'object' || !('name' in node)) return [];

  /** @type {{ id: string, level: 2 | 3, text: string }[]} */
  const result = [];

  if (node.name === 'h2' || node.name === 'h3') {
    const level = node.name === 'h2' ? 2 : 3;
    const text = collectText(node);
    const id = slugify(text);
    result.push({ id, level, text });
  }

  for (const child of node.children ?? []) {
    result.push(...extractOutline(/** @type {import('@markdoc/markdoc').RenderableTreeNode} */ (child)));
  }

  return result;
}

/**
 * @param {import('@markdoc/markdoc').RenderableTreeNode} node
 * @returns {string}
 */
function collectText(node) {
  if (node === null || node === undefined) return '';
  if (typeof node === 'string') return node;
  if (typeof node !== 'object' || !('children' in node)) return '';
  return (node.children ?? []).map(c => collectText(/** @type {import('@markdoc/markdoc').RenderableTreeNode} */ (c))).join('');
}

/** @param {string} text @returns {string} */
function slugify(text) {
  return text
    .toLowerCase()
    .replace(/[^a-z0-9\s-]/g, '')
    .replace(/\s+/g, '-')
    .replace(/-{2,}/g, '-')
    .replace(/^-|-$/g, '');
}

// ---------------------------------------------------------------------------
// File reading
// ---------------------------------------------------------------------------

/**
 * @param {string} p
 * @returns {string}
 */
function readFile(p) {
  return readFileSync(p, 'utf8');
}

// ---------------------------------------------------------------------------
// Per-chapter processing
// ---------------------------------------------------------------------------

/**
 * @typedef {{ slug: string, file: string, title: string }} ManifestChapter
 * @typedef {{ home: string, chapters: ManifestChapter[] }} ManifestAudience
 */

/**
 * Process one chapter entry from the manifest.
 *
 * @param {ManifestChapter} entry
 * @param {string} contentRoot
 * @param {Set<string>} slugs
 * @param {string[]} allErrors
 * @returns {{ slug: string, title: string, source: string, ast: import('@markdoc/markdoc').RenderableTreeNode, outline: { id: string, level: 2 | 3, text: string }[] } | null}
 */
function processChapter(entry, contentRoot, slugs, allErrors) {
  const filePath = resolve(contentRoot, entry.file);
  if (!existsSync(filePath)) {
    allErrors.push(`missing file for chapter "${entry.slug}": ${filePath}`);
    return null;
  }

  const src = readFile(filePath);
  const rawAst = Markdoc.parse(src);

  // Inject `content` attribute for {% include %} tags before transform.
  injectIncludes(rawAst, contentRoot, allErrors);

  const errors = collectErrors(rawAst, entry.file, slugs);
  allErrors.push(...errors);

  const ast = Markdoc.transform(rawAst, markdocConfig);
  const outline = extractOutline(ast);

  // Derive title: prefer frontmatter `title`, fall back to manifest title.
  const frontmatterTitle = rawAst.attributes['frontmatter']?.title;
  const title = (typeof frontmatterTitle === 'string' && frontmatterTitle) ? frontmatterTitle : entry.title;

  return {
    slug: entry.slug,
    title,
    source: relative(contentRoot, filePath),
    ast,
    outline,
  };
}

/**
 * Walk the raw AST and inject `content` into {% include %} tag nodes by
 * reading the referenced file from disk.
 *
 * @param {import('@markdoc/markdoc').Node} node
 * @param {string} contentRoot
 * @param {string[]} errors
 */
function injectIncludes(node, contentRoot, errors) {
  if (node.type === 'tag' && node.tag === 'include') {
    const file = node.attributes['file'];
    if (typeof file === 'string') {
      const includePath = resolve(contentRoot, file);
      if (!existsSync(includePath)) {
        errors.push(`include: file not found: ${includePath}`);
        node.attributes['content'] = '';
      } else {
        node.attributes['content'] = readFile(includePath);
      }
    }
  }
  for (const child of node.children ?? []) {
    injectIncludes(child, contentRoot, errors);
  }
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

async function main() {
  const manifestSrc = readFile(manifestPath);
  const manifest = /** @type {Record<string, unknown>} */ (parseToml(manifestSrc));

  mkdirSync(outJson, { recursive: true });

  const audiences = /** @type {('user' | 'admin')[]} */ (['user', 'admin']);
  let totalErrors = 0;

  for (const audience of audiences) {
    const section = /** @type {{ home: string, chapters: ManifestChapter[] } | undefined} */ (manifest[audience]);
    if (!section) {
      console.log(`[bundle] no [${audience}] section in manifest, skipping`);
      continue;
    }

    const slugs = new Set(section.chapters.map((ch) => ch.slug));
    const allErrors = /** @type {string[]} */ ([]);
    const chapters = [];

    for (const entry of section.chapters) {
      const result = processChapter(entry, contentRoot, slugs, allErrors);
      if (result) chapters.push(result);
    }

    if (allErrors.length > 0) {
      for (const e of allErrors) {
        process.stderr.write(`[bundle] error: ${e}\n`);
      }
      totalErrors += allErrors.length;
      // Continue processing other audience so we show all errors at once.
      continue;
    }

    const bundle = {
      audience,
      home: section.home,
      chapters,
    };

    const outPath = join(outJson, `${audience}.json`);
    writeFileSync(outPath, JSON.stringify(bundle, null, 2), 'utf8');
    console.log(`[bundle] wrote ${outPath} (${chapters.length} chapters)`);

    if (argv.ssr && outSsr) {
      await emitSsr(bundle, outSsr);
    }
  }

  if (totalErrors > 0) {
    process.stderr.write(`[bundle] ${totalErrors} error(s), see above\n`);
    process.exit(1);
  }
}

/**
 * Emit SSR HTML for each chapter using svelte/server render().
 *
 * @param {{ audience: string, home: string, chapters: unknown[] }} bundle
 * @param {string} outDir
 */
async function emitSsr(bundle, outDir) {
  // Dynamic import keeps the SSR path tree-shaken when --ssr is not passed.
  const { render } = await import('svelte/server');
  const { default: Manual } = await import('../src/index.js');

  mkdirSync(join(outDir, bundle.audience), { recursive: true });

  for (const chapter of bundle.chapters) {
    const ch = /** @type {{ slug: string, title: string }} */ (chapter);
    const { html, head } = render(Manual, {
      props: {
        bundle,
        slug: ch.slug,
        onNavigate: () => {},
      },
    });

    const page = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>${escapeHtml(ch.title)} - Herold Manual</title>
${head}
<link rel="stylesheet" href="/manual.css">
</head>
<body>
${html}
<script src="/manual.js" type="module"></script>
</body>
</html>`;

    const outPath = join(outDir, bundle.audience, `${ch.slug}.html`);
    writeFileSync(outPath, page, 'utf8');
    console.log(`[bundle:ssr] wrote ${outPath}`);
  }
}

/** @param {string} s @returns {string} */
function escapeHtml(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

/** @param {string} msg @returns {never} */
function fatal(msg) {
  process.stderr.write(`[bundle] ${msg}\n`);
  process.exit(1);
}

main().catch((err) => {
  process.stderr.write(`[bundle] fatal: ${err instanceof Error ? err.stack : String(err)}\n`);
  process.exit(1);
});
