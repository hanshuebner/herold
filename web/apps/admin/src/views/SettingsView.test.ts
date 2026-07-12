/**
 * SettingsView component tests (re #205).
 *
 * Locale used to be a header toggle; it is a Settings-view control now,
 * presented as a labelled dropdown (not a segmented toggle) so it scales
 * past two or three languages.
 */

import { describe, it, expect, afterEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import { settings } from '../lib/settings/settings.svelte';

describe('SettingsView', () => {
  afterEach(() => {
    settings.setLocale('en');
  });

  it('renders a labelled language dropdown with English and German options', async () => {
    const { default: SettingsView } = await import('./SettingsView.svelte');
    render(SettingsView);

    const select = screen.getByRole('combobox', { name: 'Language' });
    expect(select).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'English' })).toBeInTheDocument();
    expect(screen.getByRole('option', { name: 'Deutsch' })).toBeInTheDocument();
  });

  it('reflects the current locale as the selected option', async () => {
    settings.setLocale('de');
    const { default: SettingsView } = await import('./SettingsView.svelte');
    render(SettingsView);

    // The label text itself follows the active locale ("Sprache" once
    // set to German), so query by role alone -- there is only one
    // combobox in this view.
    expect(screen.getByRole('combobox')).toHaveValue('de');
  });

  it('changes the locale when a different option is selected', async () => {
    settings.setLocale('en');
    const { default: SettingsView } = await import('./SettingsView.svelte');
    render(SettingsView);

    await fireEvent.change(screen.getByRole('combobox', { name: 'Language' }), {
      target: { value: 'de' },
    });

    expect(settings.locale).toBe('de');
  });
});
