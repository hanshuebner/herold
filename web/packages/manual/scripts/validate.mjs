#!/usr/bin/env node
/**
 * scripts/validate.mjs
 *
 * Parses every .mdoc file under --content-root with the Markdoc schema and
 * exits non-zero on any error.  Called as a separate script for CI lint.
 *
 * Usage:
 *   node scripts/validate.mjs --content-root <dir>
 */

import { readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { parseArgs } from 'node:util';
import { createRequire } from 'node:module';

const { values: argv } = parseArgs({
  args: process.argv.slice(2),
  options: {
    'content-root': { type: 'string' },
  },
  strict: true,
});

if (!argv['content-root']) {
  process.stderr.write('[validate] --content-root is required\n');
  process.exit(1);
}

const contentRoot = resolve(/** @type {string} */ (argv['content-root']));

const require = createRequire(import.meta.url);
const Markdoc = /** @type {typeof import('@markdoc/markdoc').default} */ (
  require('@markdoc/markdoc')
);

// Inline schema (mirrors schema.ts — kept in sync manually).
const { Tag } = Markdoc;

const tags = {
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

const allowedTagNames = new Set(Object.keys(tags));
const config = { tags };

/**
 * Recursively collect all .mdoc files under a directory.
 *
 * @param {string} dir
 * @returns {string[]}
 */
function collectMdoc(dir) {
  const results = [];
  try {
    const entries = readdirSync(dir);
    for (const entry of entries) {
      const full = join(dir, entry);
      const stat = statSync(full);
      if (stat.isDirectory()) {
        results.push(...collectMdoc(full));
      } else if (entry.endsWith('.mdoc')) {
        results.push(full);
      }
    }
  } catch {
    // Directory may not exist in some test contexts; treat as empty.
  }
  return results;
}

/**
 * @param {import('@markdoc/markdoc').Node} node
 * @param {string[]} errors
 * @param {string} filePath
 */
function walkNode(node, errors, filePath) {
  if (node.type === 'tag') {
    const tagName = node.tag ?? '';
    if (tagName && !allowedTagNames.has(tagName)) {
      errors.push(`${filePath}: unknown tag "{% ${tagName} %}"`);
    }
  }
  for (const child of node.children ?? []) {
    walkNode(child, errors, filePath);
  }
}

const files = collectMdoc(contentRoot);
if (files.length === 0) {
  console.log(`[validate] no .mdoc files found under ${contentRoot}`);
  process.exit(0);
}

let totalErrors = 0;

for (const filePath of files) {
  const src = readFileSync(filePath, 'utf8');
  const ast = Markdoc.parse(src);
  const mdErrors = Markdoc.validate(ast, config).filter(
    (e) => e.error.level === 'error',
  );

  const errors = [];
  for (const err of mdErrors) {
    errors.push(`  line ${err.lines?.join('-') ?? '?'}: ${err.error.message}`);
  }
  walkNode(ast, errors, filePath);

  if (errors.length > 0) {
    process.stderr.write(`[validate] ${filePath}:\n`);
    for (const e of errors) {
      process.stderr.write(`  ${e}\n`);
    }
    totalErrors += errors.length;
  } else {
    console.log(`[validate] ok: ${filePath}`);
  }
}

if (totalErrors > 0) {
  process.stderr.write(`[validate] ${totalErrors} error(s)\n`);
  process.exit(1);
}

console.log(`[validate] all ${files.length} file(s) ok`);
