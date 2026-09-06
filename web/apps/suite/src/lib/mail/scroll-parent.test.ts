import { describe, it, expect, beforeEach } from 'vitest';
import { findScrollParent, fragmentScrollDelta } from './scroll-parent';

/**
 * Tests for findScrollParent. happy-dom's getComputedStyle reflects inline
 * styles set via element.style, so we build a minimal DOM tree and verify
 * the traversal logic directly.
 */

function makeEl(tag = 'div'): HTMLElement {
  return document.createElement(tag);
}

describe('findScrollParent', () => {
  let root: HTMLDivElement;

  beforeEach(() => {
    root = document.createElement('div');
    document.body.appendChild(root);
  });

  it('returns null when no scrollable ancestor exists', () => {
    const child = makeEl();
    const parent = makeEl();
    parent.appendChild(child);
    root.appendChild(parent);
    expect(findScrollParent(child)).toBeNull();
  });

  it('finds an ancestor with overflow-y auto', () => {
    const scrollBox = makeEl();
    scrollBox.style.overflowY = 'auto';
    const child = makeEl();
    const inner = makeEl();
    inner.appendChild(child);
    scrollBox.appendChild(inner);
    root.appendChild(scrollBox);
    expect(findScrollParent(child)).toBe(scrollBox);
  });

  it('finds an ancestor with overflow-y scroll', () => {
    const scrollBox = makeEl();
    scrollBox.style.overflowY = 'scroll';
    const child = makeEl();
    scrollBox.appendChild(child);
    root.appendChild(scrollBox);
    expect(findScrollParent(child)).toBe(scrollBox);
  });

  it('finds an ancestor with overflow-x auto', () => {
    const scrollBox = makeEl();
    scrollBox.style.overflowX = 'auto';
    const child = makeEl();
    scrollBox.appendChild(child);
    root.appendChild(scrollBox);
    expect(findScrollParent(child)).toBe(scrollBox);
  });

  it('finds an ancestor with overflow-x scroll', () => {
    const scrollBox = makeEl();
    scrollBox.style.overflowX = 'scroll';
    const child = makeEl();
    scrollBox.appendChild(child);
    root.appendChild(scrollBox);
    expect(findScrollParent(child)).toBe(scrollBox);
  });

  it('returns the nearest scrollable ancestor, not a more-distant one', () => {
    const outer = makeEl();
    outer.style.overflowY = 'auto';
    const inner = makeEl();
    inner.style.overflowY = 'scroll';
    const child = makeEl();
    inner.appendChild(child);
    outer.appendChild(inner);
    root.appendChild(outer);
    // inner is closer than outer
    expect(findScrollParent(child)).toBe(inner);
  });

  it('does not return the document element', () => {
    // No ancestor in the subtree is scrollable; the document element is excluded.
    const child = makeEl();
    document.body.appendChild(child);
    expect(findScrollParent(child)).toBeNull();
  });

  it('skips non-scrollable ancestors between el and the scrollable one', () => {
    const scrollBox = makeEl();
    scrollBox.style.overflowY = 'auto';
    const mid1 = makeEl();
    const mid2 = makeEl();
    const child = makeEl();
    mid2.appendChild(child);
    mid1.appendChild(mid2);
    scrollBox.appendChild(mid1);
    root.appendChild(scrollBox);
    expect(findScrollParent(child)).toBe(scrollBox);
  });

  it('returns null when el has no parentElement', () => {
    const detached = makeEl();
    expect(findScrollParent(detached)).toBeNull();
  });
});

// Issue #293: fragment link clicks inside the sandboxed message-body
// iframe need the outer thread scroll container moved by script, since
// the iframe never scrolls internally (its CSS height always matches its
// content's scrollHeight, so there is nothing for the browser's own
// native fragment-scroll to act on inside the iframe's own viewport).
describe('fragmentScrollDelta', () => {
  it('is zero when the target already sits at the scroll parent\'s top edge', () => {
    const frameRect = { top: 100 };
    const targetRect = { top: 50 }; // 150 in outer-viewport terms
    const parentRect = { top: 150 };
    expect(fragmentScrollDelta(frameRect, targetRect, parentRect)).toBe(0);
  });

  it('is positive when the target sits below the scroll parent\'s top edge', () => {
    const frameRect = { top: 0 };
    const targetRect = { top: 2000 };
    const parentRect = { top: 80 };
    expect(fragmentScrollDelta(frameRect, targetRect, parentRect)).toBe(1920);
  });

  it('is negative when the target sits above the scroll parent\'s top edge', () => {
    const frameRect = { top: -500 };
    const targetRect = { top: 100 };
    const parentRect = { top: 0 };
    expect(fragmentScrollDelta(frameRect, targetRect, parentRect)).toBe(-400);
  });
});
