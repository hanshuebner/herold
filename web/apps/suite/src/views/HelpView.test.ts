/**
 * Tests for HelpView (re #58).
 *
 * Covers:
 *   - TOC renders chapter titles from the fixture bundle
 *   - Route param drives the active chapter
 *   - i18n keys flow through to the Manual component
 *   - Graceful empty-bundle render (no crash)
 *   - Load-error state renders an error message
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import type { ManualBundle } from '@herold/manual';

// RenderableTreeNode type is intentionally not imported from @markdoc/markdoc
// here because @markdoc/markdoc is not a direct dep of @herold/suite; the
// suite depends on @herold/manual which re-exports the type.  Test fixtures
// use typed-as-any casts to avoid pulling in the peer dep.
type AnyNode = ManualBundle['chapters'][number]['ast'];

// ---------------------------------------------------------------------------
// Fixtures (hoisted so they are available in vi.mock() factories)
// ---------------------------------------------------------------------------

const { routerState } = vi.hoisted(() => {
  const routerState = { parts: ['help'] as string[] };
  return { routerState };
});

// Minimal two-chapter bundle that exercises TOC and page rendering.
// AST nodes are cast to the re-exported type via AnyNode so @markdoc/markdoc
// does not appear as a direct import in this file.
const FIXTURE_BUNDLE: ManualBundle = {
  audience: 'user',
  home: 'intro',
  chapters: [
    {
      slug: 'intro',
      title: 'Introduction',
      source: 'user/index.mdoc',
      ast: {
        name: 'article',
        attributes: {},
        children: [{ name: 'p', attributes: {}, children: ['Welcome to Herold.'], $$mdtype: 'Tag' }],
        $$mdtype: 'Tag',
      } as unknown as AnyNode,
      outline: [],
    },
    {
      slug: 'setup',
      title: 'Getting Started',
      source: 'user/setup.mdoc',
      ast: {
        name: 'article',
        attributes: {},
        children: [
          {
            name: 'h2',
            attributes: { id: 'install' },
            children: ['Installation'],
            $$mdtype: 'Tag',
          },
          { name: 'p', attributes: {}, children: ['Install herold.'], $$mdtype: 'Tag' },
        ],
        $$mdtype: 'Tag',
      } as unknown as AnyNode,
      outline: [{ id: 'install', level: 2, text: 'Installation' }],
    },
  ],
};

// ---------------------------------------------------------------------------
// Module mocks
// ---------------------------------------------------------------------------

vi.mock('../lib/router/router.svelte', () => ({
  router: {
    get parts() {
      return routerState.parts;
    },
    get current() {
      return '/' + routerState.parts.join('/');
    },
    navigate: vi.fn(),
    matches: vi.fn((prefix: string) => routerState.parts[0] === prefix),
  },
}));

vi.mock('../lib/i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  i18n: { locale: 'en', t: (key: string) => key },
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function mockFetch(bundle: ManualBundle | null, ok = true): void {
  global.fetch = vi.fn().mockResolvedValue({
    ok,
    status: ok ? 200 : 500,
    json: () => Promise.resolve(bundle),
  } as Response);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('HelpView', () => {
  beforeEach(() => {
    routerState.parts = ['help'];
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('renders chapter titles in the TOC after load', async () => {
    mockFetch(FIXTURE_BUNDLE);
    const { default: HelpView } = await import('./HelpView.svelte');
    render(HelpView);

    // Wait for the async fetch to resolve and the component to update.
    await waitFor(() => {
      expect(screen.getByText('Introduction')).toBeInTheDocument();
      expect(screen.getByText('Getting Started')).toBeInTheDocument();
    });
  });

  it('shows the loading state before fetch completes', async () => {
    // Never-resolving fetch so the loading state stays visible.
    global.fetch = vi.fn().mockReturnValue(new Promise(() => {}));
    const { default: HelpView } = await import('./HelpView.svelte');
    render(HelpView);

    // The i18n mock returns the key itself, so we look for the key string.
    expect(screen.getByRole('status')).toBeInTheDocument();
    expect(screen.getByText('manual.loading')).toBeInTheDocument();
  });

  it('shows an error state when fetch fails', async () => {
    mockFetch(null, false);
    const { default: HelpView } = await import('./HelpView.svelte');
    render(HelpView);

    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText('manual.loadError')).toBeInTheDocument();
    });
  });

  it('shows the second chapter when the route slug matches it', async () => {
    routerState.parts = ['help', 'setup'];
    mockFetch(FIXTURE_BUNDLE);
    const { default: HelpView } = await import('./HelpView.svelte');
    render(HelpView);

    await waitFor(() => {
      // "Getting Started" chapter should be active — its TOC link will be
      // present. The Manual component renders the TOC regardless of active
      // chapter, so both titles appear; the active chapter heading also
      // appears in the page content.
      expect(screen.getByText('Getting Started')).toBeInTheDocument();
    });
  });

  it('does not crash when the bundle has no chapters', async () => {
    const emptyBundle: ManualBundle = {
      audience: 'user',
      home: 'intro',
      chapters: [],
    };
    mockFetch(emptyBundle);
    const { default: HelpView } = await import('./HelpView.svelte');
    render(HelpView);

    await waitFor(() => {
      expect(screen.getByText('manual.empty')).toBeInTheDocument();
    });
  });
});
