/**
 * Shell component tests (re #205).
 *
 * The top bar used to render a `lang-switch` radiogroup next to
 * sign-out, letting the operator flip locale directly from the header.
 * Locale is a Settings-view control now (`SettingsView.svelte`); the
 * header keeps only the wordmark and sign-out/principal-email controls,
 * and gains a "Settings" nav-rail entry that routes there.
 */

import { describe, it, expect, afterEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';

function setHash(hash: string): void {
  window.location.hash = hash;
  window.dispatchEvent(new HashChangeEvent('hashchange'));
}

describe('Shell', () => {
  afterEach(() => {
    setHash('#/dashboard');
  });

  it('does not render a language radiogroup in the top bar', async () => {
    const { default: Shell } = await import('./Shell.svelte');
    render(Shell);

    expect(screen.queryByRole('radiogroup')).not.toBeInTheDocument();
    expect(screen.queryByText('EN')).not.toBeInTheDocument();
    expect(screen.queryByText('DE')).not.toBeInTheDocument();
  });

  it('renders a Settings nav-rail entry linking to /settings', async () => {
    const { default: Shell } = await import('./Shell.svelte');
    render(Shell);

    expect(screen.getByRole('button', { name: 'Settings' })).toBeInTheDocument();
  });
});
