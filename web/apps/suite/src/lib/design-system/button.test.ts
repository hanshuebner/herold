/**
 * Shared Button component tests (@herold/design-system).
 *
 * The button is the single migration target for the identity-settings
 * surface (Item 1 of the settings rework). These tests pin the contract
 * the migration relies on: variant class, type, disabled, click, leading
 * icon, accessible label.
 */

import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import Button from '@herold/design-system/Button.svelte';

describe('design-system Button', () => {
  it('renders the label text', () => {
    const { getByRole } = render(Button, {
      props: { children: undefined },
    });
    // No children -> still a button element.
    expect(getByRole('button')).toBeInTheDocument();
  });

  it('applies the variant class', () => {
    const { getByRole } = render(Button, { props: { variant: 'primary' } });
    expect(getByRole('button').className).toContain('ds-btn-primary');
  });

  it('defaults to the secondary variant', () => {
    const { getByRole } = render(Button, {});
    expect(getByRole('button').className).toContain('ds-btn-secondary');
  });

  it('forwards the type attribute', () => {
    const { getByRole } = render(Button, { props: { type: 'submit' } });
    expect(getByRole('button')).toHaveAttribute('type', 'submit');
  });

  it('defaults to type=button', () => {
    const { getByRole } = render(Button, {});
    expect(getByRole('button')).toHaveAttribute('type', 'button');
  });

  it('honours the disabled prop', () => {
    const { getByRole } = render(Button, { props: { disabled: true } });
    expect(getByRole('button')).toBeDisabled();
  });

  it('invokes onclick when clicked', async () => {
    const onclick = vi.fn();
    const { getByRole } = render(Button, { props: { onclick } });
    await fireEvent.click(getByRole('button'));
    expect(onclick).toHaveBeenCalledOnce();
  });

  it('does not invoke onclick when disabled', async () => {
    const onclick = vi.fn();
    const { getByRole } = render(Button, {
      props: { onclick, disabled: true },
    });
    await fireEvent.click(getByRole('button'));
    expect(onclick).not.toHaveBeenCalled();
  });

  it('exposes the aria-label for icon-only buttons', () => {
    const { getByLabelText } = render(Button, {
      props: { ariaLabel: 'Edit identity' },
    });
    expect(getByLabelText('Edit identity')).toBeInTheDocument();
  });

  it('applies the compact modifier class', () => {
    const { getByRole } = render(Button, { props: { compact: true } });
    expect(getByRole('button').className).toContain('compact');
  });

  it('forwards the testid hook', () => {
    const { getByTestId } = render(Button, { props: { testid: 'save-btn' } });
    expect(getByTestId('save-btn')).toBeInTheDocument();
  });
});
