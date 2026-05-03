/**
 * Canonical JSON bundle shape produced by scripts/bundle.mjs and consumed
 * by the Manual Svelte component.
 *
 * The shape is locked by the contract between the bundler (this package) and
 * the parallel content-migration task.  Changes here require a coordinated
 * update in scripts/bundle.mjs and all consumers.
 */
import type { RenderableTreeNode } from '@markdoc/markdoc';

/**
 * A single heading entry in the right-rail "on this page" outline.
 * Only h2 and h3 are surfaced; deeper headings are not included.
 */
export interface OutlineEntry {
  id: string;
  level: 2 | 3;
  text: string;
}

/**
 * A single chapter in the manual.
 *
 * `ast` is the Markdoc RenderableTreeNode produced by
 * `Markdoc.transform(Markdoc.parse(source), config)`.  It is serialised as
 * JSON in the bundle file and deserialised at render time.
 *
 * `outline` is extracted from the AST during bundling so the right-rail
 * outline can be rendered without walking the full AST.
 */
export interface ManualChapter {
  slug: string;
  title: string;
  /** Relative path under the content root (e.g. "user/install.mdoc"). */
  source: string;
  ast: RenderableTreeNode;
  outline: OutlineEntry[];
}

/**
 * The complete bundle for one audience ("user" or "admin").
 *
 * This is the top-level type serialised to `user.json` / `admin.json` by
 * scripts/bundle.mjs and passed directly as the `bundle` prop to
 * <Manual />.
 */
export interface ManualBundle {
  audience: 'user' | 'admin';
  /** Slug of the chapter to show when no slug is provided. */
  home: string;
  chapters: ManualChapter[];
}
