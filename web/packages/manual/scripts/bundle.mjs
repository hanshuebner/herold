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
 *     [--ssr]                 also emit per-chapter HTML via Markdoc renderers.html()
 *
 * Exits non-zero on any validation error.
 *
 * SSR mode (--ssr) uses Markdoc's built-in renderers.html() to produce
 * pure static HTML without any Svelte runtime dependency.  Each custom
 * tag (callout, code-group, include, req, kbd) is rendered via a
 * transform that maps the Tag to a standard HTML element with
 * appropriate attributes and class names.  The Svelte components in
 * src/components/tags/ remain authoritative for the in-app render path;
 * the SSR path intentionally uses the simpler HTML mapping.
 *
 * SSR output layout (under --out-ssr <dir>):
 *   <out-ssr>/user/index.html          redirect to first user chapter
 *   <out-ssr>/user/<slug>/index.html   per-chapter full HTML page
 *   <out-ssr>/admin/...                same shape
 *   <out-ssr>/manual/index.html        redirect to /manual/user/
 *   <out-ssr>/manual.css               shared CSS
 *   <out-ssr>/manual.js                tiny interactivity (TOC filter + smoothscroll)
 */

import { readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs';
import { resolve, join, dirname, relative, extname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';
import { createRequire } from 'node:module';

// Resolve design-system source files relative to this script's location.
// The design-system package lives at web/packages/design-system/src/
// relative to the repo root; this script is at web/packages/manual/scripts/
// so the relative path is ../../design-system/src/.
const DESIGN_SYSTEM_SRC = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '../../design-system/src',
);

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
// TOML parsing (tiny subset - only what manifest.toml needs)
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

// ---------------------------------------------------------------------------
// Tag schemas (shared between JSON and SSR paths)
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// SSR-specific tag schemas: transform custom Tags -> plain HTML elements
//
// The SSR path does NOT use the Svelte components from src/components/tags/.
// Instead each custom tag is mapped to a standard HTML structure.  This
// keeps the SSR path free of any Svelte/Vite dependency.
// ---------------------------------------------------------------------------

/** @param {typeof import('@markdoc/markdoc').default} M */
function buildSsrTagSchemas(M) {
  const { Tag } = M;
  return {
    callout: {
      render: 'aside',
      children: ['paragraph', 'fence', 'list'],
      attributes: {
        type: { type: String, default: 'info', matches: ['info', 'warning', 'caution'], errorLevel: 'error' },
        title: { type: String },
      },
      transform(node, config) {
        const type = node.attributes['type'] ?? 'info';
        const title = node.attributes['title'];
        const children = node.transformChildren(config);
        const innerChildren = title
          ? [new Tag('strong', { class: 'callout-title' }, [title]), ...children]
          : children;
        return new Tag('aside', { class: `callout callout--${type}` }, innerChildren);
      },
    },
    'code-group': {
      render: 'div',
      children: ['fence'],
      attributes: {},
      transform(node, config) {
        return new Tag('div', { class: 'code-group' }, node.transformChildren(config));
      },
    },
    include: {
      render: 'pre',
      selfClosing: true,
      attributes: {
        file: { type: String, required: true, errorLevel: 'error' },
        lang: { type: String, default: 'text' },
        content: { type: String },
      },
      transform(node, _config) {
        const lang = node.attributes['lang'] ?? 'text';
        const content = node.attributes['content'] ?? '';
        const file = node.attributes['file'] ?? '';
        return new Tag('pre', { class: `language-${lang}`, 'data-file': file },
          [new Tag('code', {}, [content])]);
      },
    },
    req: {
      render: 'span',
      selfClosing: true,
      attributes: {
        id: { type: String, required: true, errorLevel: 'error' },
      },
      transform(node, _config) {
        const id = node.attributes['id'] ?? '';
        return new Tag('span', { class: 'req-id' }, [id]);
      },
    },
    kbd: {
      render: 'kbd',
      selfClosing: true,
      attributes: {
        keys: { type: String, required: true, errorLevel: 'error' },
      },
      transform(node, _config) {
        const keys = node.attributes['keys'] ?? '';
        // Render each key as its own <kbd> inside a wrapper span.
        const keyParts = keys.split(/\s+/).filter(Boolean);
        const inner = keyParts.flatMap((k, i) => {
          const kbdTag = new Tag('kbd', {}, [k]);
          return i === 0 ? [kbdTag] : ['+', kbdTag];
        });
        return new Tag('span', { class: 'kbd-combo' }, inner);
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
      errors.push(`${filePath}: unknown tag "{% ${tagName} %}" - add it to src/markdoc/schema.ts or remove it`);
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
      emitSsr(bundle, outSsr);
    }
  }

  if (totalErrors > 0) {
    process.stderr.write(`[bundle] ${totalErrors} error(s), see above\n`);
    process.exit(1);
  }

  // Emit shared SSR assets after processing all audiences.
  if (argv.ssr && outSsr) {
    emitSsrSharedAssets(outSsr);
  }
}

// ---------------------------------------------------------------------------
// SSR HTML emission via Markdoc renderers.html()
//
// This path requires NO compiled Svelte, NO Vite build, NO node-gyp.
// It uses the same Markdoc transform pipeline as the JSON path but with
// an SSR-specific tag schema that maps each custom tag to a standard
// HTML element instead of a Svelte component name.
// ---------------------------------------------------------------------------

/**
 * Emit SSR HTML for each chapter using Markdoc renderers.html().
 * No Svelte dependency -- pure static HTML with semantic class names
 * that manual.css styles.
 *
 * @param {{ audience: string, home: string, chapters: Array<{slug: string, title: string, source: string, ast: unknown, outline: Array<{id: string, level: number, text: string}>}> }} bundle
 * @param {string} outDir
 */
function emitSsr(bundle, outDir) {
  const ssrTags = buildSsrTagSchemas(Markdoc);
  const ssrConfig = { tags: ssrTags };

  mkdirSync(join(outDir, bundle.audience), { recursive: true });

  // Build a TOC list of all chapters for the sidebar.
  const tocItems = bundle.chapters.map((ch) => {
    return { slug: ch.slug, title: ch.title };
  });

  // Emit per-chapter pages.
  for (const chapter of bundle.chapters) {
    const ch = /** @type {{ slug: string, title: string, source: string, outline: Array<{id: string, level: number, text: string}> }} */ (chapter);

    // Re-process the chapter from source using SSR-specific tag transforms.
    // We read the source file again to get the raw AST and re-transform it
    // with the SSR schemas so the HTML output uses plain elements.
    const filePath = resolve(contentRoot, ch.source);
    const src = readFile(filePath);
    const rawAst = Markdoc.parse(src);
    const ssrErrors = /** @type {string[]} */ ([]);
    injectIncludes(rawAst, contentRoot, ssrErrors);
    const ssrAst = Markdoc.transform(rawAst, ssrConfig);
    const contentHtml = Markdoc.renderers.html(ssrAst);

    // Build the TOC sidebar HTML.
    const tocHtml = buildTocHtml(tocItems, ch.slug, bundle.audience);

    // Build the on-this-page rail (headings from this chapter).
    const onThisPageHtml = buildOnThisPageHtml(ch.outline);

    const page = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>${escapeHtml(ch.title)} - Herold Manual</title>
<link rel="stylesheet" href="/manual.css">
</head>
<body class="manual-page">
<nav class="manual-nav" aria-label="Manual navigation">
<div class="manual-nav-header">
<a href="/manual/" class="manual-home-link">Herold Manual</a>
<div class="manual-audience-tabs">
<a href="/manual/user/" class="${bundle.audience === 'user' ? 'active' : ''}">User</a>
<a href="/manual/admin/" class="${bundle.audience === 'admin' ? 'active' : ''}">Admin</a>
</div>
</div>
<div class="manual-toc-search">
<input type="search" id="toc-search" placeholder="Filter chapters..." aria-label="Filter chapters">
</div>
<ul class="manual-toc" id="manual-toc">
${tocHtml}
</ul>
</nav>
<main class="manual-main">
<article class="manual-content">
${contentHtml}
</article>
${onThisPageHtml ? `<nav class="manual-on-this-page" aria-label="On this page">
<h2>On this page</h2>
<ul>
${onThisPageHtml}
</ul>
</nav>` : ''}
</main>
<script src="/manual.js" type="module"></script>
</body>
</html>`;

    const outPath = join(outDir, bundle.audience, ch.slug, 'index.html');
    mkdirSync(dirname(outPath), { recursive: true });
    writeFileSync(outPath, page, 'utf8');
    console.log(`[bundle:ssr] wrote ${outPath}`);
  }

  // Emit audience index redirect (-> home chapter).
  const homeSlug = bundle.home;
  const audienceIndexPath = join(outDir, bundle.audience, 'index.html');
  const audienceIndexHtml = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="0;url=/manual/${bundle.audience}/${homeSlug}/">
<title>Herold Manual - ${bundle.audience === 'user' ? 'User' : 'Admin'}</title>
</head>
<body>
<p>Redirecting to <a href="/manual/${bundle.audience}/${homeSlug}/">manual home</a>...</p>
<script>location.replace('/manual/${bundle.audience}/${homeSlug}/');</script>
</body>
</html>`;
  writeFileSync(audienceIndexPath, audienceIndexHtml, 'utf8');
  console.log(`[bundle:ssr] wrote ${audienceIndexPath}`);
}

/**
 * Emit shared assets: manual.css and manual.js.
 * Also emits the top-level /manual/index.html redirect.
 *
 * manual.css is built by prepending the design-system's tokens.css and
 * reset.css (read directly from web/packages/design-system/src/) before
 * the standalone-manual layout rules (MANUAL_LAYOUT_CSS below).  This
 * gives the standalone manual the same Carbon-derived color, spacing,
 * typography, and motion tokens as the suite and admin SPAs.  IBM Plex
 * fonts are NOT included (the font files are not shipped in the manual
 * dist tree); the design-system's system-ui fallback stack applies.
 *
 * @param {string} outDir
 */
function emitSsrSharedAssets(outDir) {
  mkdirSync(join(outDir, 'manual'), { recursive: true });

  // Top-level /manual/index.html -> redirect to user manual.
  const manualIndexPath = join(outDir, 'manual', 'index.html');
  const manualIndexHtml = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta http-equiv="refresh" content="0;url=/manual/user/">
<title>Herold Manual</title>
</head>
<body>
<p>Redirecting to <a href="/manual/user/">user manual</a>...</p>
<script>location.replace('/manual/user/');</script>
</body>
</html>`;
  writeFileSync(manualIndexPath, manualIndexHtml, 'utf8');
  console.log(`[bundle:ssr] wrote ${manualIndexPath}`);

  // manual.css -- shared stylesheet for the standalone manual.
  //
  // Layer 1: design-system tokens (Carbon-derived CSS custom properties).
  // Layer 2: design-system reset (box-sizing, margin, base typography).
  // Layer 3: standalone manual layout + component rules (MANUAL_LAYOUT_CSS).
  //
  // Reading tokens.css and reset.css directly from the source tree avoids
  // duplicating the authoritative token values here.  When the design system
  // is updated the manual re-bundling picks up the new values automatically.
  let designSystemTokens = '';
  let designSystemReset = '';
  const tokensPath = join(DESIGN_SYSTEM_SRC, 'tokens', 'tokens.css');
  const resetPath = join(DESIGN_SYSTEM_SRC, 'reset.css');
  if (existsSync(tokensPath)) {
    designSystemTokens = readFileSync(tokensPath, 'utf8');
  } else {
    process.stderr.write(`[bundle:ssr] warning: design-system tokens not found at ${tokensPath}; manual.css will use fallback colours\n`);
  }
  if (existsSync(resetPath)) {
    // The reset sets font-family: var(--font-sans).  For the standalone manual
    // we override just the font-family on body to the system-ui fallback stack
    // because the IBM Plex woff2 files are not shipped in the manual dist.
    designSystemReset = readFileSync(resetPath, 'utf8');
  }

  const fullCss = [
    '/* Herold standalone manual -- generated by web/packages/manual/scripts/bundle.mjs */',
    '',
    '/* === Layer 1: design-system tokens === */',
    designSystemTokens,
    '',
    '/* === Layer 2: design-system reset === */',
    designSystemReset,
    '',
    '/* Override body font: IBM Plex is not bundled in the manual dist tree;',
    '   the system-ui fallback already declared in --font-sans applies. */',
    'body { font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }',
    '',
    '/* === Layer 3: standalone manual layout === */',
    MANUAL_LAYOUT_CSS,
  ].join('\n');

  const cssPath = join(outDir, 'manual.css');
  writeFileSync(cssPath, fullCss, 'utf8');
  console.log(`[bundle:ssr] wrote ${cssPath}`);

  // manual.js -- tiny interactivity (TOC search filter + smooth scroll).
  const jsPath = join(outDir, 'manual.js');
  writeFileSync(jsPath, MANUAL_JS, 'utf8');
  console.log(`[bundle:ssr] wrote ${jsPath}`);
}

/**
 * Build the TOC sidebar HTML list items.
 *
 * @param {Array<{slug: string, title: string}>} items
 * @param {string} activeSlug
 * @param {string} audience
 * @returns {string}
 */
function buildTocHtml(items, activeSlug, audience) {
  return items.map((item) => {
    const activeClass = item.slug === activeSlug ? ' class="active"' : '';
    return `<li${activeClass}><a href="/manual/${audience}/${item.slug}/">${escapeHtml(item.title)}</a></li>`;
  }).join('\n');
}

/**
 * Build the on-this-page rail HTML list items.
 *
 * @param {Array<{id: string, level: number, text: string}>} outline
 * @returns {string}
 */
function buildOnThisPageHtml(outline) {
  if (!outline || outline.length === 0) return '';
  return outline.map((entry) => {
    const levelClass = `otp-h${entry.level}`;
    return `<li class="${levelClass}"><a href="#${escapeHtml(entry.id)}">${escapeHtml(entry.text)}</a></li>`;
  }).join('\n');
}

/** @param {string} s @returns {string} */
function escapeHtml(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

// ---------------------------------------------------------------------------
// Embedded CSS and JS for the standalone manual
// ---------------------------------------------------------------------------

// MANUAL_LAYOUT_CSS: standalone manual layout rules.
// Colors and typography use the design-system CSS custom properties defined in
// tokens.css, which is prepended at bundle time by emitSsrSharedAssets().
// Do not add hard-coded colour values here; use var(--token-name) throughout.
const MANUAL_LAYOUT_CSS = `/* manual layout -- generated by web/packages/manual/scripts/bundle.mjs */
:root {
  --manual-sidebar-width: 260px;
  --manual-otp-width: 200px;
}

body.manual-page {
  display: grid;
  grid-template-columns: var(--manual-sidebar-width) 1fr var(--manual-otp-width);
  grid-template-rows: 1fr;
  min-height: 100vh;
  font-size: 16px;
  line-height: 1.6;
  color: var(--text-primary);
  background: var(--background);
}

/* Sidebar nav */
.manual-nav {
  grid-column: 1;
  grid-row: 1;
  position: sticky;
  top: 0;
  height: 100vh;
  overflow-y: auto;
  background: var(--layer-01);
  border-right: 1px solid var(--border-subtle-01);
  padding: var(--spacing-05);
  display: flex;
  flex-direction: column;
  gap: var(--spacing-04);
}

.manual-nav-header {
  display: flex;
  flex-direction: column;
  gap: var(--spacing-03);
}

.manual-home-link {
  font-weight: 700;
  font-size: 1.1rem;
  color: var(--text-primary);
  text-decoration: none;
}

.manual-home-link:hover { text-decoration: underline; }

.manual-audience-tabs {
  display: flex;
  gap: var(--spacing-03);
}

.manual-audience-tabs a {
  font-size: 0.85rem;
  padding: 0.2rem 0.6rem;
  border-radius: var(--radius-sm);
  text-decoration: none;
  color: var(--interactive);
  border: 1px solid var(--border-subtle-01);
}

.manual-audience-tabs a.active {
  background: var(--interactive);
  color: var(--text-on-color);
  border-color: var(--interactive);
}

.manual-toc-search input {
  width: 100%;
  padding: 0.4rem 0.6rem;
  font-size: 0.85rem;
  border: 1px solid var(--border-subtle-01);
  border-radius: var(--radius-sm);
  font-family: inherit;
  background: var(--field-01);
  color: var(--text-primary);
}

.manual-toc {
  list-style: none;
  display: flex;
  flex-direction: column;
  gap: 0.1rem;
}

.manual-toc li a {
  display: block;
  padding: 0.3rem 0.6rem;
  border-radius: var(--radius-sm);
  text-decoration: none;
  color: var(--text-primary);
  font-size: 0.9rem;
}

.manual-toc li a:hover { background: var(--layer-hover-01); }
.manual-toc li.active > a {
  background: var(--interactive);
  color: var(--text-on-color);
}

/* Main content */
.manual-main {
  grid-column: 2;
  grid-row: 1;
  padding: var(--spacing-08) var(--spacing-09);
  max-width: 800px;
  overflow-x: hidden;
}

.manual-content h1,
.manual-content h2,
.manual-content h3,
.manual-content h4 {
  margin-top: 1.5em;
  margin-bottom: 0.5em;
  line-height: 1.3;
  color: var(--text-primary);
}

.manual-content h1 { font-size: 1.8rem; }
.manual-content h2 { font-size: 1.4rem; border-bottom: 1px solid var(--border-subtle-01); padding-bottom: 0.3em; }
.manual-content h3 { font-size: 1.1rem; }

.manual-content p { margin-bottom: 1em; }

.manual-content a { color: var(--link-primary); }
.manual-content a:hover { color: var(--link-primary-hover); }

.manual-content code {
  font-family: var(--font-mono);
  font-size: 0.88em;
  background: var(--layer-02);
  padding: 0.1em 0.3em;
  border-radius: var(--radius-xs);
}

.manual-content pre {
  font-family: var(--font-mono);
  font-size: 0.88em;
  background: var(--layer-02);
  padding: var(--spacing-05);
  border-radius: var(--radius-md);
  overflow-x: auto;
  margin-bottom: 1em;
}

.manual-content pre code {
  background: none;
  padding: 0;
  font-size: inherit;
}

.manual-content ul,
.manual-content ol {
  margin-bottom: 1em;
  padding-left: 1.5em;
}

.manual-content li { margin-bottom: 0.25em; }

/* Callout component */
.callout {
  padding: var(--spacing-05);
  border-radius: var(--radius-md);
  margin-bottom: 1em;
  border-left: 4px solid currentColor;
}

.callout--info {
  background: color-mix(in srgb, var(--support-info) 15%, transparent);
  color: var(--support-info);
}
.callout--warning {
  background: color-mix(in srgb, var(--support-warning) 15%, transparent);
  color: var(--support-warning);
}
.callout--caution {
  background: color-mix(in srgb, var(--support-error) 15%, transparent);
  color: var(--support-error);
}

.callout-title { display: block; font-weight: 700; margin-bottom: 0.4em; }

/* Code group */
.code-group {
  margin-bottom: 1em;
  border: 1px solid var(--border-subtle-01);
  border-radius: var(--radius-md);
  overflow: hidden;
}

.code-group pre { border-radius: 0; margin: 0; }

/* Req reference */
.req-id {
  font-family: var(--font-mono);
  font-size: 0.85em;
  background: var(--layer-02);
  padding: 0.1em 0.4em;
  border-radius: var(--radius-xs);
  color: var(--text-helper);
}

/* Keyboard shortcut */
.kbd-combo { display: inline-flex; align-items: center; gap: 2px; }
.kbd-combo kbd {
  font-family: var(--font-mono);
  font-size: 0.8em;
  background: var(--layer-02);
  border: 1px solid var(--border-subtle-01);
  border-radius: var(--radius-xs);
  padding: 0.1em 0.4em;
  box-shadow: 0 1px 0 var(--border-subtle-01);
}

/* On-this-page rail */
.manual-on-this-page {
  grid-column: 3;
  grid-row: 1;
  position: sticky;
  top: 0;
  height: 100vh;
  overflow-y: auto;
  padding: var(--spacing-08) var(--spacing-05);
  font-size: 0.85rem;
}

.manual-on-this-page h2 {
  font-size: 0.75rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-secondary);
  margin-bottom: 0.75rem;
}

.manual-on-this-page ul { list-style: none; }
.manual-on-this-page li { margin-bottom: 0.25rem; }
.manual-on-this-page a { color: var(--text-primary); text-decoration: none; }
.manual-on-this-page a:hover { color: var(--link-primary); }
.otp-h3 { padding-left: 0.75rem; }

/* Responsive: collapse sidebar on small screens */
@media (max-width: 900px) {
  body.manual-page {
    grid-template-columns: 1fr;
    grid-template-rows: auto 1fr;
  }
  .manual-nav {
    grid-column: 1;
    position: static;
    height: auto;
    border-right: none;
    border-bottom: 1px solid var(--border-subtle-01);
  }
  .manual-main { grid-column: 1; padding: 1rem; max-width: none; }
  .manual-on-this-page { display: none; }
}
`;

const MANUAL_JS = `// manual.js -- minimal interactivity for the standalone Herold manual.
// No framework, no JMAP, no auth -- just TOC search and smooth scroll.

(function () {
  'use strict';

  // TOC search filter
  const searchInput = document.getElementById('toc-search');
  const tocList = document.getElementById('manual-toc');

  if (searchInput && tocList) {
    searchInput.addEventListener('input', function () {
      const q = this.value.toLowerCase().trim();
      const items = tocList.querySelectorAll('li');
      items.forEach(function (li) {
        const text = (li.textContent || '').toLowerCase();
        li.style.display = q === '' || text.includes(q) ? '' : 'none';
      });
    });
  }

  // Smooth scroll for on-this-page links (headings already have id from Markdoc).
  document.addEventListener('click', function (e) {
    const target = e.target;
    if (!(target instanceof HTMLAnchorElement)) return;
    const href = target.getAttribute('href');
    if (!href || !href.startsWith('#')) return;
    const anchor = document.getElementById(href.slice(1));
    if (!anchor) return;
    e.preventDefault();
    anchor.scrollIntoView({ behavior: 'smooth', block: 'start' });
    history.pushState(null, '', href);
  });
})();
`;

/** @param {string} msg @returns {never} */
function fatal(msg) {
  process.stderr.write(`[bundle] ${msg}\n`);
  process.exit(1);
}

main().catch((err) => {
  process.stderr.write(`[bundle] fatal: ${err instanceof Error ? err.stack : String(err)}\n`);
  process.exit(1);
});
