/**
 * Shared Header component tests (@herold/design-system, re #205).
 *
 * Header.svelte is the app-header shell both the suite's GlobalBar and
 * the admin SPA's Shell consume so the two apps share one component for
 * the outer header chrome instead of each reimplementing the flex row /
 * height / background / border-bottom independently. These tests pin the
 * contract both consumers rely on: the `brand` snippet and default
 * content render as siblings inside a single `<header>`, in that order,
 * and an app-supplied `class` is applied so the consumer's own
 * `:global()`-scoped styles (padding, gap, layout) can still reach it.
 */

import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import Header from '@herold/design-system/Header.svelte';

function textSnippet(text: string) {
  return createRawSnippet(() => ({
    render: () => `<span>${text}</span>`,
  }));
}

describe('design-system Header', () => {
  it('renders a single <header> element', () => {
    const { container } = render(Header, { props: {} });
    expect(container.querySelectorAll('header')).toHaveLength(1);
  });

  it('applies an app-supplied class alongside its own base class', () => {
    const { container } = render(Header, { props: { class: 'global-bar' } });
    const header = container.querySelector('header')!;
    expect(header.classList.contains('ds-header')).toBe(true);
    expect(header.classList.contains('global-bar')).toBe(true);
  });

  it('renders the brand snippet before the default content, as siblings', () => {
    const { container } = render(Header, {
      props: {
        brand: textSnippet('brand-mark'),
        children: textSnippet('rest-of-bar'),
      },
    });
    const header = container.querySelector('header')!;
    expect(header.children).toHaveLength(2);
    expect(header.children[0]!.textContent).toBe('brand-mark');
    expect(header.children[1]!.textContent).toBe('rest-of-bar');
  });

  it('renders only the default content when brand is omitted', () => {
    const { container } = render(Header, {
      props: { children: textSnippet('rest-of-bar') },
    });
    const header = container.querySelector('header')!;
    expect(header.children).toHaveLength(1);
    expect(header.textContent?.trim()).toBe('rest-of-bar');
  });
});
