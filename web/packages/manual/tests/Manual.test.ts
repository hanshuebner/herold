/**
 * Manual component tests.
 *
 * Uses @testing-library/svelte + happy-dom.  Exercises the rendered output
 * of the Manual, ManualToc, ManualOnThisPage, ManualSearch, and ManualPage
 * components against a fixture bundle.
 */
import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import type { ManualBundle } from '../src/markdoc/bundle.js';

// Build a minimal fixture bundle without running the full bundler so tests
// are fast and hermetic.
import Markdoc from '@markdoc/markdoc';

/**
 * Build a minimal ManualBundle from raw Markdoc source strings.
 * Uses the real Markdoc transform so AST shapes match production.
 */
function makeBundle(
  audience: 'user' | 'admin',
  chapters: Array<{ slug: string; title: string; src: string }>,
): ManualBundle {
  const { Tag } = Markdoc;
  const markdocChapters = chapters.map((ch) => {
    const ast = Markdoc.transform(Markdoc.parse(ch.src), {});
    // Extract outline manually (mirrors extractOutline in bundler).
    function extractOutline(
      node: Markdoc.RenderableTreeNode,
    ): Array<{ id: string; level: 2 | 3; text: string }> {
      if (node === null || typeof node === 'string') return [];
      if (!('name' in node)) return [];
      const result: Array<{ id: string; level: 2 | 3; text: string }> = [];
      if (node.name === 'h2' || node.name === 'h3') {
        const level = node.name === 'h2' ? 2 : 3;
        const text = collectText(node);
        const id = text.toLowerCase().replace(/\s+/g, '-').replace(/[^a-z0-9-]/g, '');
        result.push({ id, level: level as 2 | 3, text });
      }
      for (const child of node.children ?? []) {
        result.push(
          ...extractOutline(child as Markdoc.RenderableTreeNode),
        );
      }
      return result;
    }

    function collectText(node: Markdoc.RenderableTreeNode): string {
      if (node === null || node === undefined) return '';
      if (typeof node === 'string') return node;
      if (!('children' in node)) return '';
      return (node.children ?? [])
        .map((c) => collectText(c as Markdoc.RenderableTreeNode))
        .join('');
    }

    return {
      slug: ch.slug,
      title: ch.title,
      source: `${audience}/${ch.slug}.mdoc`,
      ast,
      outline: extractOutline(ast),
    };
  });

  return {
    audience,
    home: chapters[0]?.slug ?? 'index',
    chapters: markdocChapters,
  };
}

const FIXTURE_BUNDLE = makeBundle('user', [
  {
    slug: 'index',
    title: 'Introduction',
    src: `# Introduction\n\n## Getting started\n\nWelcome.\n\n### First steps\n\nCreate an account.\n`,
  },
  {
    slug: 'install',
    title: 'Installation',
    src: `# Installation\n\n## Requirements\n\nLinux or macOS.\n`,
  },
  {
    slug: 'settings',
    title: 'Settings',
    src: `# Settings\n\n## General\n\nConfigure general settings.\n`,
  },
]);

// ---------------------------------------------------------------------------
// Manual component
// ---------------------------------------------------------------------------

describe('Manual component', () => {
  it('renders the TOC with all chapter titles', async () => {
    const onNavigate = vi.fn();
    const { default: Manual } = await import('../src/components/Manual.svelte');
    render(Manual, {
      props: {
        bundle: FIXTURE_BUNDLE,
        slug: null,
        onNavigate,
      },
    });

    // Chapter titles appear in both the TOC buttons and the page heading;
    // use getAllByText and assert at least one match per title.
    expect(screen.getAllByText('Introduction').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Installation').length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText('Settings').length).toBeGreaterThanOrEqual(1);
  });

  it('renders the home chapter by default (slug=null)', async () => {
    const onNavigate = vi.fn();
    const { default: Manual } = await import('../src/components/Manual.svelte');
    render(Manual, {
      props: {
        bundle: FIXTURE_BUNDLE,
        slug: null,
        onNavigate,
      },
    });

    // The home chapter ("index") content should appear in the page area.
    const article = document.querySelector('[data-slug="index"]');
    expect(article).toBeInTheDocument();
  });

  it('renders the specified chapter when slug is provided', async () => {
    const onNavigate = vi.fn();
    const { default: Manual } = await import('../src/components/Manual.svelte');
    render(Manual, {
      props: {
        bundle: FIXTURE_BUNDLE,
        slug: 'install',
        onNavigate,
      },
    });

    const article = document.querySelector('[data-slug="install"]');
    expect(article).toBeInTheDocument();
  });

  it('calls onNavigate when a TOC item is clicked', async () => {
    const onNavigate = vi.fn();
    const { default: Manual } = await import('../src/components/Manual.svelte');
    render(Manual, {
      props: {
        bundle: FIXTURE_BUNDLE,
        slug: null,
        onNavigate,
      },
    });

    // Click "Installation" in the TOC.
    const installBtn = screen.getByRole('button', { name: 'Installation' });
    await fireEvent.click(installBtn);
    expect(onNavigate).toHaveBeenCalledWith('install');
  });

  it('filters TOC when search query is entered', async () => {
    const onNavigate = vi.fn();
    const { default: Manual } = await import('../src/components/Manual.svelte');
    render(Manual, {
      props: {
        bundle: FIXTURE_BUNDLE,
        slug: null,
        onNavigate,
      },
    });

    const searchInput = screen.getByRole('searchbox');
    await fireEvent.input(searchInput, { target: { value: 'install' } });

    // Only "Installation" should remain visible in the TOC.
    expect(screen.getByText('Installation')).toBeInTheDocument();
    // "Introduction" and "Settings" should be filtered out.
    expect(screen.queryByRole('button', { name: 'Introduction' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Settings' })).not.toBeInTheDocument();
  });

  it('shows no-results message when search matches nothing', async () => {
    const onNavigate = vi.fn();
    const { default: Manual } = await import('../src/components/Manual.svelte');
    render(Manual, {
      props: {
        bundle: FIXTURE_BUNDLE,
        slug: null,
        onNavigate,
      },
    });

    const searchInput = screen.getByRole('searchbox');
    await fireEvent.input(searchInput, { target: { value: 'xyzzy-no-match' } });

    expect(screen.getByText('No matching topics.')).toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// ManualToc
// ---------------------------------------------------------------------------

describe('ManualToc', () => {
  it('marks the current chapter as active', async () => {
    const { default: ManualToc } = await import('../src/components/ManualToc.svelte');
    const onNavigate = vi.fn();
    render(ManualToc, {
      props: {
        chapters: FIXTURE_BUNDLE.chapters,
        currentSlug: 'install',
        searchQuery: '',
        onNavigate,
        t: (key: string) => key,
      },
    });

    const activeBtn = screen.getByRole('button', { name: 'Installation' });
    expect(activeBtn).toHaveAttribute('aria-current', 'page');

    const inactiveBtn = screen.getByRole('button', { name: 'Introduction' });
    expect(inactiveBtn).not.toHaveAttribute('aria-current');
  });
});

// ---------------------------------------------------------------------------
// ManualOnThisPage
// ---------------------------------------------------------------------------

describe('ManualOnThisPage', () => {
  it('renders outline entries', async () => {
    const { default: ManualOnThisPage } = await import('../src/components/ManualOnThisPage.svelte');
    const onNavigate = vi.fn();
    const outline = [
      { id: 'getting-started', level: 2 as const, text: 'Getting started' },
      { id: 'first-steps', level: 3 as const, text: 'First steps' },
    ];
    render(ManualOnThisPage, {
      props: {
        outline,
        onNavigate,
        t: (key: string) => key,
      },
    });

    expect(screen.getByText('Getting started')).toBeInTheDocument();
    expect(screen.getByText('First steps')).toBeInTheDocument();
  });

  it('calls onNavigate with heading id when clicked', async () => {
    const { default: ManualOnThisPage } = await import('../src/components/ManualOnThisPage.svelte');
    const onNavigate = vi.fn();
    const outline = [
      { id: 'getting-started', level: 2 as const, text: 'Getting started' },
    ];
    render(ManualOnThisPage, {
      props: {
        outline,
        onNavigate,
        t: (key: string) => key,
      },
    });

    await fireEvent.click(screen.getByText('Getting started'));
    expect(onNavigate).toHaveBeenCalledWith('getting-started');
  });

  it('renders nothing when outline is empty', async () => {
    const { default: ManualOnThisPage } = await import('../src/components/ManualOnThisPage.svelte');
    const onNavigate = vi.fn();
    const { container } = render(ManualOnThisPage, {
      props: {
        outline: [],
        onNavigate,
        t: (key: string) => key,
      },
    });

    expect(container.querySelector('nav')).not.toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// ManualSearch
// ---------------------------------------------------------------------------

describe('ManualSearch', () => {
  it('renders with the correct placeholder', async () => {
    const { default: ManualSearch } = await import('../src/components/ManualSearch.svelte');
    const onQuery = vi.fn();
    render(ManualSearch, {
      props: {
        query: '',
        onQuery,
        t: (key: string) => (key === 'manual.search.placeholder' ? 'Search topics...' : key),
      },
    });

    const input = screen.getByRole('searchbox');
    expect(input).toHaveAttribute('placeholder', 'Search topics...');
  });

  it('calls onQuery when input changes', async () => {
    const { default: ManualSearch } = await import('../src/components/ManualSearch.svelte');
    const onQuery = vi.fn();
    render(ManualSearch, {
      props: {
        query: '',
        onQuery,
        t: (key: string) => key,
      },
    });

    const input = screen.getByRole('searchbox');
    await fireEvent.input(input, { target: { value: 'hello' } });
    expect(onQuery).toHaveBeenCalledWith('hello');
  });
});

// ---------------------------------------------------------------------------
// ManualPage AST rendering
// ---------------------------------------------------------------------------

describe('ManualPage', () => {
  it('renders heading text', async () => {
    const { default: ManualPage } = await import('../src/components/ManualPage.svelte');
    const chapter = FIXTURE_BUNDLE.chapters.find((c) => c.slug === 'index')!;
    render(ManualPage, { props: { chapter, t: (key: string) => key } });

    expect(screen.getByText('Introduction')).toBeInTheDocument();
    expect(screen.getByText('Getting started')).toBeInTheDocument();
    expect(screen.getByText('First steps')).toBeInTheDocument();
  });

  it('does not use innerHTML injection', async () => {
    const { default: ManualPage } = await import('../src/components/ManualPage.svelte');
    // Craft a chapter where if innerHTML were used the script would run.
    // Because we use text nodes only, this should render as visible text.
    const xssBundle = makeBundle('user', [
      {
        slug: 'xss',
        title: 'XSS Test',
        src: `# XSS Test\n\nHello <script>alert(1)</script> world.\n`,
      },
    ]);
    const chapter = xssBundle.chapters[0]!;
    const { container } = render(ManualPage, { props: { chapter, t: (key: string) => key } });

    // The script tag text should appear as text content, not as an element.
    const scriptEls = container.querySelectorAll('script');
    // Any script elements should be from Svelte internals, not from content.
    // We verify no user-content script is present by checking text context.
    const articleEl = container.querySelector('[data-slug="xss"]');
    expect(articleEl).toBeInTheDocument();
    // The raw text of the paragraph should not have executed as HTML.
    // (happy-dom does not execute scripts, but we assert the element count
    // to confirm the content node is text, not an injected script element.)
    const innerScripts = articleEl?.querySelectorAll('script');
    expect(innerScripts?.length ?? 0).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// Tokenizer
// ---------------------------------------------------------------------------

describe('tokenize', () => {
  it('tokenizes TOML correctly', async () => {
    const { tokenize } = await import('../src/markdoc/tokenize.js');
    const tokens = tokenize('[server]\nbind = "0.0.0.0:8080"', 'toml');
    const types = tokens.map((t) => t.type);
    expect(types).toContain('section');
    expect(types).toContain('key');
    expect(types).toContain('string');
  });

  it('tokenizes JSON correctly', async () => {
    const { tokenize } = await import('../src/markdoc/tokenize.js');
    const tokens = tokenize('{"key": "value", "n": 42, "ok": true}', 'json');
    const types = tokens.map((t) => t.type);
    expect(types).toContain('key');
    expect(types).toContain('string');
    expect(types).toContain('number');
    expect(types).toContain('keyword');
  });

  it('tokenizes Go correctly', async () => {
    const { tokenize } = await import('../src/markdoc/tokenize.js');
    const tokens = tokenize('package main\n\nimport "fmt"\n\nfunc main() {\n\tfmt.Println("hello")\n}', 'go');
    const types = tokens.map((t) => t.type);
    expect(types).toContain('keyword');
    expect(types).toContain('string');
  });

  it('tokenizes bash correctly', async () => {
    const { tokenize } = await import('../src/markdoc/tokenize.js');
    const tokens = tokenize('#!/bin/bash\n# comment\nexport FOO="bar"\nif true; then\n  echo "hi"\nfi', 'bash');
    const types = tokens.map((t) => t.type);
    expect(types).toContain('comment');
    expect(types).toContain('keyword');
    expect(types).toContain('string');
  });

  it('returns a single text token for unknown languages', async () => {
    const { tokenize } = await import('../src/markdoc/tokenize.js');
    const tokens = tokenize('hello world', 'cobol');
    expect(tokens).toHaveLength(1);
    expect(tokens[0]?.type).toBe('text');
  });

  it('handles empty input without throwing', async () => {
    const { tokenize } = await import('../src/markdoc/tokenize.js');
    expect(() => tokenize('', 'go')).not.toThrow();
    expect(() => tokenize('', 'unknown')).not.toThrow();
  });
});

// ---------------------------------------------------------------------------
// defaultT
// ---------------------------------------------------------------------------

describe('defaultT', () => {
  it('returns the English string for known keys', async () => {
    const { defaultT } = await import('../src/chrome/strings.js');
    expect(defaultT('manual.toc.label')).toBe('Table of contents');
  });

  it('returns the key itself for unknown keys', async () => {
    const { defaultT } = await import('../src/chrome/strings.js');
    expect(defaultT('manual.unknown.key')).toBe('manual.unknown.key');
  });
});
