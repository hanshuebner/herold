/**
 * SettingsView component tests (re #205).
 *
 * Locale used to be a header toggle; it is a Settings-view control now,
 * mirroring the Suite's Appearance-section language segmented control.
 */

import { describe, it, expect, afterEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import { settings } from '../lib/settings/settings.svelte';

describe('SettingsView', () => {
  afterEach(() => {
    settings.setLocale('en');
  });

  it('renders a language radiogroup with English and German options', async () => {
    const { default: SettingsView } = await import('./SettingsView.svelte');
    render(SettingsView);

    expect(screen.getByRole('radiogroup', { name: 'Language' })).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: 'English' })).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: 'Deutsch' })).toBeInTheDocument();
  });

  it('reflects the current locale as the checked radio', async () => {
    settings.setLocale('de');
    const { default: SettingsView } = await import('./SettingsView.svelte');
    render(SettingsView);

    expect(screen.getByRole('radio', { name: 'Deutsch' })).toHaveAttribute('aria-checked', 'true');
  });

  it('changes the locale when a different language button is clicked', async () => {
    settings.setLocale('en');
    const { default: SettingsView } = await import('./SettingsView.svelte');
    render(SettingsView);

    await fireEvent.click(screen.getByRole('radio', { name: 'Deutsch' }));

    expect(settings.locale).toBe('de');
  });
});
