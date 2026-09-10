/**
 * Regression test for issue #308: the expanded "Labels" group in the Suite
 * mail sidebar rendered its label rows, the empty-state row, and the "+
 * Neues Label" add-row at the same left indent as the system mailbox rows
 * (Posteingang, Gesendet, ...) above them, so the group's hierarchy was not
 * visible beyond the disclosure triangle on the header.
 *
 * App.svelte is a large shell component that is impractical to mount in
 * isolation for a pure CSS-layout assertion (see the puppeteer verification
 * in the issue comment for the rendered/measured proof). This test guards
 * the source-level invariant instead: every row inside
 * `.mailbox-list.custom` (label rows via `li button`, and the empty-state
 * row via `li.empty`) must carry a `padding-left` strictly greater than the
 * base `.mailbox-list li button` padding-left, so the group reads as
 * indented relative to the system mailboxes and the "Labels" header.
 */

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const appSveltePath = join(dirname(fileURLToPath(import.meta.url)), 'App.svelte');
const source = readFileSync(appSveltePath, 'utf-8');

/** Extracts the `<style>` block content of App.svelte. */
function styleBlock(): string {
  const match = source.match(/<style[^>]*>([\s\S]*?)<\/style>/);
  if (!match) throw new Error('App.svelte has no <style> block');
  return match[1]!;
}

/**
 * Extracts the declaration block for the first rule whose selector list
 * (verbatim, including any commas/newlines) equals `selector`.
 */
function ruleBody(css: string, selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const re = new RegExp(`${escaped}\\s*\\{([^}]*)\\}`);
  const match = css.match(re);
  if (!match) throw new Error(`no CSS rule found for selector: ${selector}`);
  return match[1]!;
}

/** Extracts the value of `padding-left` (or the left component of shorthand
 * `padding`) from a declaration block, resolving spacing custom-property
 * tokens against the design-system pixel scale so values are comparable. */
const SPACING_PX: Record<string, number> = {
  '--spacing-01': 2,
  '--spacing-02': 4,
  '--spacing-03': 8,
  '--spacing-04': 12,
  '--spacing-05': 16,
  '--spacing-06': 24,
  '--spacing-07': 32,
};

function tokenToPx(token: string): number {
  const varMatch = token.match(/var\((--spacing-\d\d)\)/);
  if (!varMatch) throw new Error(`unrecognised spacing token: ${token}`);
  const name = varMatch[1]!;
  const px = SPACING_PX[name];
  if (px === undefined) throw new Error(`unknown spacing token: ${name}`);
  return px;
}

function paddingLeftPx(declBlock: string): number {
  const explicit = declBlock.match(/padding-left:\s*([^;]+);/);
  if (explicit) return tokenToPx(explicit[1]!.trim());
  const shorthand = declBlock.match(/padding:\s*([^;]+);/);
  if (shorthand) {
    const parts = shorthand[1]!.trim().split(/\s+/);
    // CSS padding shorthand: 1 value = all sides, 2 = v/h, 3 = top/h/bottom,
    // 4 = top/right/bottom/left.
    if (parts.length === 1) return tokenToPx(parts[0]!);
    if (parts.length === 2) return tokenToPx(parts[1]!);
    if (parts.length === 3) return tokenToPx(parts[1]!);
    return tokenToPx(parts[3]!);
  }
  throw new Error('declaration block has no padding-left or padding shorthand');
}

describe('sidebar Labels group row indent (re #308)', () => {
  const css = styleBlock();
  const basePaddingLeft = paddingLeftPx(ruleBody(css, '.mailbox-list li button'));

  it('base mailbox-list row padding-left is the spacing-04 token (sanity check)', () => {
    expect(basePaddingLeft).toBe(12);
  });

  it('label rows (.mailbox-list.custom li button) are indented past the base row padding', () => {
    const indented = paddingLeftPx(ruleBody(css, '.mailbox-list.custom li button'));
    expect(indented).toBeGreaterThan(basePaddingLeft);
  });

  it('the empty-state row (.mailbox-list.custom li.empty) matches the label-row indent', () => {
    const labelIndent = paddingLeftPx(ruleBody(css, '.mailbox-list.custom li button'));
    const emptyIndent = paddingLeftPx(ruleBody(css, '.mailbox-list.custom li.empty'));
    expect(emptyIndent).toBe(labelIndent);
  });

  it('the add-row ("+ Neues Label") button has no separate lesser-indent override', () => {
    // .mailbox-list.custom .add-row button only styles color/font-weight, so
    // it inherits padding-left from .mailbox-list.custom li button above --
    // guard against a future narrower override reintroducing the flush edge.
    const addRowBody = ruleBody(css, '.mailbox-list.custom .add-row button');
    expect(addRowBody).not.toMatch(/padding(-left)?:/);
  });
});
