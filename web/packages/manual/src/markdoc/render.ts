/**
 * Helpers for traversing the Markdoc RenderableTreeNode tree.
 *
 * The Manual component renders the AST by switching on node type.  These
 * utilities provide type-safe accessors so the Svelte templates stay
 * declarative.
 *
 * We use the `$$mdtype` discriminator rather than `instanceof Tag` to avoid
 * cross-module identity issues when the Tag class is imported via ESM in
 * Vite/Vitest while the AST was produced via a CJS require().
 */
import type { RenderableTreeNode, Tag } from '@markdoc/markdoc';

/** True if the node is a Markdoc Tag (not a plain string or null). */
export function isTag(node: RenderableTreeNode): node is Tag {
  return (
    node !== null &&
    typeof node === 'object' &&
    (node as Record<string, unknown>)['$$mdtype'] === 'Tag'
  );
}

/** True if the node is a plain text string. */
export function isText(node: RenderableTreeNode): node is string {
  return typeof node === 'string';
}

/**
 * Return child nodes of a Tag, or an empty array for primitives.
 *
 * Markdoc children can be null, string, or Tag; we normalise away null here
 * so callers can always iterate safely.
 */
export function children(node: RenderableTreeNode): RenderableTreeNode[] {
  if (!isTag(node)) return [];
  const raw = (node as unknown as { children?: unknown[] }).children ?? [];
  return raw.filter((c) => c !== null) as RenderableTreeNode[];
}

/**
 * Return a named attribute from a Tag, or undefined.
 * Type parameter is purely a convenience cast for the caller.
 */
export function attr<T = unknown>(node: Tag, name: string): T | undefined {
  const attrs = (node as unknown as { attributes: Record<string, unknown> }).attributes;
  return attrs[name] as T | undefined;
}

/**
 * Collect the plain-text content of a node and all its descendants.
 * Used to derive heading `id` slugs and outline text.
 */
export function textContent(node: RenderableTreeNode): string {
  if (node === null || node === undefined) return '';
  if (isText(node)) return node;
  if (!isTag(node)) return '';
  const raw = (node as unknown as { children?: unknown[] }).children ?? [];
  return raw
    .map((c) => textContent(c as RenderableTreeNode))
    .join('');
}

/**
 * Convert a heading text string to a URL-safe id slug.
 *
 * Rules: lowercase, strip non-alphanumeric except hyphens, collapse
 * consecutive hyphens, trim leading/trailing hyphens.
 */
export function slugify(text: string): string {
  return text
    .toLowerCase()
    .replace(/[^a-z0-9\s-]/g, '')
    .replace(/\s+/g, '-')
    .replace(/-{2,}/g, '-')
    .replace(/^-|-$/g, '');
}
