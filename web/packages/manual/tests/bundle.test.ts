/**
 * Bundler tests.
 *
 * Exercises scripts/bundle.mjs by calling the internal processing logic
 * directly (not via child_process) to keep tests fast and deterministic.
 */
import { describe, it, expect } from 'vitest';
import { createRequire } from 'node:module';
import { resolve, join } from 'node:path';
import { existsSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { mkdtempSync, writeFileSync, mkdirSync } from 'node:fs';

const fixtureRoot = resolve(__dirname, 'fixtures');

// We test the bundle by shelling out to the script so we exercise the real
// Node.js entry point.  Use spawnSync so we can capture exit code.
import { spawnSync } from 'node:child_process';

const bundleScript = resolve(__dirname, '../scripts/bundle.mjs');
const validateScript = resolve(__dirname, '../scripts/validate.mjs');

function runBundle(args: string[]): { exitCode: number; stdout: string; stderr: string } {
  const result = spawnSync(
    process.execPath,
    [bundleScript, ...args],
    { encoding: 'utf8', cwd: resolve(__dirname, '..') },
  );
  return {
    exitCode: result.status ?? 1,
    stdout: result.stdout ?? '',
    stderr: result.stderr ?? '',
  };
}

function runValidate(args: string[]): { exitCode: number; stdout: string; stderr: string } {
  const result = spawnSync(
    process.execPath,
    [validateScript, ...args],
    { encoding: 'utf8', cwd: resolve(__dirname, '..') },
  );
  return {
    exitCode: result.status ?? 1,
    stdout: result.stdout ?? '',
    stderr: result.stderr ?? '',
  };
}

// ---------------------------------------------------------------------------
// Happy path
// ---------------------------------------------------------------------------

describe('bundle happy path', () => {
  const outDir = mkdtempSync(join(tmpdir(), 'herold-manual-test-'));

  it('exits 0 on valid fixtures', () => {
    const r = runBundle([
      '--manifest', join(fixtureRoot, 'manifest.toml'),
      '--content-root', fixtureRoot,
      '--out-json', outDir,
    ]);
    expect(r.exitCode, `stderr: ${r.stderr}`).toBe(0);
  });

  it('writes user.json', () => {
    expect(existsSync(join(outDir, 'user.json'))).toBe(true);
  });

  it('writes admin.json', () => {
    expect(existsSync(join(outDir, 'admin.json'))).toBe(true);
  });

  it('user.json has correct shape', () => {
    const raw = readFileSync(join(outDir, 'user.json'), 'utf8');
    const bundle = JSON.parse(raw) as {
      audience: string;
      home: string;
      chapters: Array<{
        slug: string;
        title: string;
        source: string;
        ast: unknown;
        outline: Array<{ id: string; level: number; text: string }>;
      }>;
    };
    expect(bundle.audience).toBe('user');
    expect(bundle.home).toBe('index');
    expect(bundle.chapters.length).toBeGreaterThanOrEqual(3);
  });

  it('chapter has outline entries for h2 and h3 headings', () => {
    const raw = readFileSync(join(outDir, 'user.json'), 'utf8');
    const bundle = JSON.parse(raw) as {
      chapters: Array<{
        slug: string;
        outline: Array<{ id: string; level: number; text: string }>;
      }>;
    };
    const indexChapter = bundle.chapters.find((c) => c.slug === 'index');
    expect(indexChapter).toBeDefined();
    expect(indexChapter!.outline.some((e) => e.level === 2)).toBe(true);
    expect(indexChapter!.outline.some((e) => e.level === 3)).toBe(true);
    // h1 headings should NOT appear in the outline
    expect(indexChapter!.outline.some((e) => e.level === 1)).toBe(false);
  });

  it('frontmatter title is extracted', () => {
    const raw = readFileSync(join(outDir, 'user.json'), 'utf8');
    const bundle = JSON.parse(raw) as {
      chapters: Array<{ slug: string; title: string }>;
    };
    const installChapter = bundle.chapters.find((c) => c.slug === 'install');
    expect(installChapter?.title).toBe('Installation');
  });

  it('include tag inlines file content into the AST', () => {
    const raw = readFileSync(join(outDir, 'user.json'), 'utf8');
    const bundle = JSON.parse(raw) as {
      chapters: Array<{
        slug: string;
        ast: Record<string, unknown>;
      }>;
    };
    const installChapter = bundle.chapters.find((c) => c.slug === 'install');
    expect(installChapter).toBeDefined();
    // The AST should contain an IncludedCode node with content from the snippet.
    const astStr = JSON.stringify(installChapter!.ast);
    expect(astStr).toContain('IncludedCode');
    expect(astStr).toContain('herold');
  });

  it('source field is a relative path', () => {
    const raw = readFileSync(join(outDir, 'user.json'), 'utf8');
    const bundle = JSON.parse(raw) as {
      chapters: Array<{ slug: string; source: string }>;
    };
    for (const ch of bundle.chapters) {
      expect(ch.source).not.toMatch(/^\//, 'source must be relative, not absolute');
    }
  });
});

// ---------------------------------------------------------------------------
// Unknown tag rejection
// ---------------------------------------------------------------------------

describe('bundle unknown-tag rejection', () => {
  it('exits non-zero when a .mdoc file contains an unknown tag', () => {
    const tmp = mkdtempSync(join(tmpdir(), 'herold-manual-reject-'));
    const contentDir = join(tmp, 'content');
    mkdirSync(contentDir);
    mkdirSync(join(contentDir, 'user'));

    // Write a chapter with an unknown tag
    writeFileSync(join(contentDir, 'user', 'bad.mdoc'), `---
title: Bad chapter
slug: bad
audience: user
---

# Bad

{% unknown-tag %}
This tag is not in the schema.
{% /unknown-tag %}
`);

    const manifestPath = join(tmp, 'manifest.toml');
    writeFileSync(manifestPath, `[user]
home = "bad"

  [[user.chapters]]
  slug = "bad"
  file = "user/bad.mdoc"
  title = "Bad"
`);

    const outDir = join(tmp, 'out');
    const r = runBundle([
      '--manifest', manifestPath,
      '--content-root', contentDir,
      '--out-json', outDir,
    ]);
    expect(r.exitCode).not.toBe(0);
    expect(r.stderr).toContain('unknown tag');
  });
});

// ---------------------------------------------------------------------------
// Missing include rejection
// ---------------------------------------------------------------------------

describe('bundle missing-include rejection', () => {
  it('exits non-zero when {% include %} references a missing file', () => {
    const tmp = mkdtempSync(join(tmpdir(), 'herold-manual-include-'));
    const contentDir = join(tmp, 'content');
    mkdirSync(contentDir);
    mkdirSync(join(contentDir, 'user'));

    writeFileSync(join(contentDir, 'user', 'broken.mdoc'), `---
title: Broken
slug: broken
audience: user
---

# Broken

{% include file="does-not-exist.toml" lang="toml" /%}
`);

    const manifestPath = join(tmp, 'manifest.toml');
    writeFileSync(manifestPath, `[user]
home = "broken"

  [[user.chapters]]
  slug = "broken"
  file = "user/broken.mdoc"
  title = "Broken"
`);

    const outDir = join(tmp, 'out');
    const r = runBundle([
      '--manifest', manifestPath,
      '--content-root', contentDir,
      '--out-json', outDir,
    ]);
    expect(r.exitCode).not.toBe(0);
    expect(r.stderr).toContain('not found');
  });
});

// ---------------------------------------------------------------------------
// Invalid href rejection
// ---------------------------------------------------------------------------

describe('bundle invalid href rejection', () => {
  it('exits non-zero when a link uses an invalid href scheme', () => {
    const tmp = mkdtempSync(join(tmpdir(), 'herold-manual-href-'));
    const contentDir = join(tmp, 'content');
    mkdirSync(contentDir);
    mkdirSync(join(contentDir, 'user'));

    writeFileSync(join(contentDir, 'user', 'badhref.mdoc'), `---
title: Bad Href
slug: badhref
audience: user
---

# Bad Href

See [ftp link](ftp://example.com) for details.
`);

    const manifestPath = join(tmp, 'manifest.toml');
    writeFileSync(manifestPath, `[user]
home = "badhref"

  [[user.chapters]]
  slug = "badhref"
  file = "user/badhref.mdoc"
  title = "Bad Href"
`);

    const outDir = join(tmp, 'out');
    const r = runBundle([
      '--manifest', manifestPath,
      '--content-root', contentDir,
      '--out-json', outDir,
    ]);
    expect(r.exitCode).not.toBe(0);
    expect(r.stderr).toContain('invalid href');
  });
});

// ---------------------------------------------------------------------------
// Missing file rejection
// ---------------------------------------------------------------------------

describe('bundle missing chapter file rejection', () => {
  it('exits non-zero when a manifest chapter file does not exist', () => {
    const tmp = mkdtempSync(join(tmpdir(), 'herold-manual-missing-'));
    const contentDir = join(tmp, 'content');
    mkdirSync(contentDir);
    mkdirSync(join(contentDir, 'user'));

    const manifestPath = join(tmp, 'manifest.toml');
    writeFileSync(manifestPath, `[user]
home = "ghost"

  [[user.chapters]]
  slug = "ghost"
  file = "user/ghost.mdoc"
  title = "Ghost"
`);

    const outDir = join(tmp, 'out');
    const r = runBundle([
      '--manifest', manifestPath,
      '--content-root', contentDir,
      '--out-json', outDir,
    ]);
    expect(r.exitCode).not.toBe(0);
    expect(r.stderr).toContain('missing file');
  });
});

// ---------------------------------------------------------------------------
// validate.mjs
// ---------------------------------------------------------------------------

describe('validate script', () => {
  it('exits 0 on fixture directory', () => {
    const r = runValidate(['--content-root', fixtureRoot]);
    expect(r.exitCode, `stderr: ${r.stderr}`).toBe(0);
  });

  it('exits non-zero on a directory with an unknown tag', () => {
    const tmp = mkdtempSync(join(tmpdir(), 'herold-validate-'));
    writeFileSync(join(tmp, 'bad.mdoc'), `# Bad\n{% nope %}\nbad\n{% /nope %}\n`);
    const r = runValidate(['--content-root', tmp]);
    expect(r.exitCode).not.toBe(0);
    expect(r.stderr).toContain('unknown tag');
  });

  it('exits 0 when no .mdoc files are present', () => {
    const tmp = mkdtempSync(join(tmpdir(), 'herold-validate-empty-'));
    const r = runValidate(['--content-root', tmp]);
    expect(r.exitCode).toBe(0);
  });
});
