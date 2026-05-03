/**
 * HelpView component tests.
 *
 * Exercises the /help route view against a fixture bundle:
 *   - TOC renders all chapter titles
 *   - Chapter content renders when a slug is active
 *   - Fallback error state when the bundle fetch fails
 *   - Route param (slug from router.parts) selects the correct chapter
 *
 * The router singleton uses window.location.hash; in happy-dom the hash is
 * writable so we update it directly between tests to simulate navigation.
 *
 * The fixture bundle is constructed as plain objects matching the Markdoc
 * RenderableTreeNode / Tag shape ($$mdtype: 'Tag') so we do not need to
 * import @markdoc/markdoc here -- the admin SPA does not have it as a
 * direct dependency and adding it only for tests would be wasteful.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/svelte';
import type { ManualBundle } from '@herold/manual';

// ---------------------------------------------------------------------------
// Fixture bundle helpers
//
// Construct a minimal ManualBundle using the Markdoc Tag plain-object shape.
// The $$mdtype discriminator is what isTag() in @herold/manual/src/markdoc/render.ts
// checks, so plain objects with this property render correctly.
// ---------------------------------------------------------------------------

type TagNode = {
  $$mdtype: 'Tag';
  name: string;
  attributes: Record<string, unknown>;
  children: (TagNode | string | null)[];
};

function tag(name: string, children: (TagNode | string | null)[] = [], attributes: Record<string, unknown> = {}): TagNode {
  return { $$mdtype: 'Tag', name, attributes, children };
}

/**
 * Build a trivial chapter AST: a document root with an h1 and a paragraph.
 */
function makeChapter(slug: string, title: string, body: string) {
  const ast = tag('document', [
    tag('h1', [title]),
    tag('p', [body]),
  ]);
  return {
    slug,
    title,
    source: `admin/${slug}.mdoc`,
    // TagNode is structurally compatible with RenderableTreeNode (same $$mdtype shape).
    // The double cast avoids importing @markdoc/markdoc which is not a dep of @herold/admin.
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ast: ast as any,
    outline: [] as Array<{ id: string; level: 2 | 3; text: string }>,
  };
}

const FIXTURE_BUNDLE: ManualBundle = {
  audience: 'admin',
  home: 'index',
  chapters: [
    makeChapter('index', 'Welcome', 'Welcome to the admin manual.'),
    makeChapter('install', 'Installation', 'Linux or macOS.'),
    makeChapter('operate', 'Operating Herold', 'Run herold.'),
  ],
};

// ---------------------------------------------------------------------------
// fetch mock setup
// ---------------------------------------------------------------------------

function mockFetchOk(bundle: ManualBundle): void {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue({
    ok: true,
    json: () => Promise.resolve(bundle),
  } as unknown as Response);
}

function mockFetchError(status: number): void {
  vi.spyOn(globalThis, 'fetch').mockResolvedValue({
    ok: false,
    status,
  } as unknown as Response);
}

// ---------------------------------------------------------------------------
// Router state helpers
//
// The router singleton reads window.location.hash on construction and on
// hashchange. In happy-dom we set hash and dispatch the event to simulate
// navigation to a help route.
// ---------------------------------------------------------------------------

function setHash(hash: string): void {
  window.location.hash = hash;
  window.dispatchEvent(new HashChangeEvent('hashchange'));
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('HelpView', () => {
  beforeEach(() => {
    // Start each test on the help route so the router's state is /help.
    setHash('#/help');
  });

  afterEach(() => {
    vi.restoreAllMocks();
    // Clean up: reset to dashboard to avoid polluting subsequent tests.
    setHash('#/dashboard');
  });

  it('renders the TOC with all chapter titles when bundle loads successfully', async () => {
    mockFetchOk(FIXTURE_BUNDLE);

    const { default: HelpView } = await import('./HelpView.svelte');
    render(HelpView);

    // Wait for async bundle load; at least one element with this text must appear.
    await waitFor(() => {
      expect(screen.getAllByText('Welcome').length).toBeGreaterThanOrEqual(1);
    });

    // All chapter titles should appear in the TOC as buttons.
    expect(screen.getByRole('button', { name: 'Welcome' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Installation' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Operating Herold' })).toBeInTheDocument();
  });

  it('shows loading state initially', async () => {
    // Make fetch never resolve so we can inspect the loading state.
    vi.spyOn(globalThis, 'fetch').mockReturnValue(new Promise(() => {}));

    const { default: HelpView } = await import('./HelpView.svelte');
    render(HelpView);

    expect(screen.getByText('Loading manual...')).toBeInTheDocument();
  });

  it('shows error message when bundle fetch returns a non-ok status', async () => {
    mockFetchError(404);

    const { default: HelpView } = await import('./HelpView.svelte');
    render(HelpView);

    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
    });

    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent('Manual unavailable');
    expect(alert).toHaveTextContent('HTTP 404');
  });

  it('shows error message when fetch throws a network error', async () => {
    vi.spyOn(globalThis, 'fetch').mockRejectedValue(new Error('Network failure'));

    const { default: HelpView } = await import('./HelpView.svelte');
    render(HelpView);

    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
    });

    expect(screen.getByRole('alert')).toHaveTextContent('Network failure');
  });

  it('renders the home chapter when slug is absent (#/help)', async () => {
    mockFetchOk(FIXTURE_BUNDLE);
    setHash('#/help');

    const { default: HelpView } = await import('./HelpView.svelte');
    render(HelpView);

    // The home chapter is 'index' (Welcome); the article data-slug should be present.
    await waitFor(() => {
      const article = document.querySelector('[data-slug="index"]');
      expect(article).toBeInTheDocument();
    });
  });

  it('renders the correct chapter when slug is #/help/install', async () => {
    mockFetchOk(FIXTURE_BUNDLE);
    setHash('#/help/install');

    const { default: HelpView } = await import('./HelpView.svelte');
    render(HelpView);

    await waitFor(() => {
      // The install chapter content should render.
      const article = document.querySelector('[data-slug="install"]');
      expect(article).toBeInTheDocument();
    });
  });

  it('renders the operate chapter when slug is #/help/operate', async () => {
    mockFetchOk(FIXTURE_BUNDLE);
    setHash('#/help/operate');

    const { default: HelpView } = await import('./HelpView.svelte');
    render(HelpView);

    await waitFor(() => {
      const article = document.querySelector('[data-slug="operate"]');
      expect(article).toBeInTheDocument();
    });
  });

  it('fetches from /admin/manual/admin.json', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(FIXTURE_BUNDLE),
    } as unknown as Response);

    const { default: HelpView } = await import('./HelpView.svelte');
    render(HelpView);

    await waitFor(() => {
      expect(fetchSpy).toHaveBeenCalledWith('/admin/manual/admin.json');
    });
  });
});
