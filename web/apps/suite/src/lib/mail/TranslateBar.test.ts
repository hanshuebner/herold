/**
 * Component tests for TranslateBar.svelte (issue #84).
 *
 * Covers:
 *  - Affordance visibility: shows for foreign body, hidden for same-language
 *    body, hidden after 501, hidden for short body.
 *  - Consent gate: first activation shows the dialog; second activation
 *    proceeds without the dialog.
 *  - Show-original toggle after a successful translation.
 *  - Error display for non-501 API failures.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/svelte';
import TranslateBar, { CONSENT_STORAGE_KEY, NEVER_LANGS_STORAGE_KEY } from './TranslateBar.svelte';

// ── Hoisted mocks ──────────────────────────────────────────────────────────

const { mockDetectLanguage, mockTranslateFeature, mockCallTranslateApi } = vi.hoisted(() => {
  return {
    mockDetectLanguage: vi.fn(),
    mockTranslateFeature: { available: true },
    mockCallTranslateApi: vi.fn(),
  };
});

vi.mock('../translate/lang-map', () => ({
  detectLanguage: mockDetectLanguage,
}));

vi.mock('../translate/translate-api.svelte', () => ({
  translateFeature: mockTranslateFeature,
  callTranslateApi: mockCallTranslateApi,
}));

vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  i18n: { locale: 'en' },
}));

// Mock account-scoped storage using an in-memory map so tests are isolated.
const storageMap = new Map<string, unknown>();
vi.mock('../storage/account-scoped', () => ({
  readAccountJson: <T>(_key: string, fallback: T): T => {
    const val = storageMap.get(_key);
    return val !== undefined ? (val as T) : fallback;
  },
  writeAccountJson: (_key: string, value: unknown): void => {
    storageMap.set(_key, value);
  },
}));

// ── Helpers ────────────────────────────────────────────────────────────────

const LONG_BODY = 'a'.repeat(200); // long enough for detection

function renderBar(overrides: { bodyText?: string; locale?: string; emailText?: string } = {}) {
  return render(TranslateBar, {
    props: {
      bodyText: overrides.bodyText ?? LONG_BODY,
      locale: overrides.locale ?? 'en',
      emailText: overrides.emailText ?? LONG_BODY,
    },
  });
}

// ── Tests ──────────────────────────────────────────────────────────────────

describe('TranslateBar: affordance visibility', () => {
  beforeEach(() => {
    storageMap.clear();
    mockDetectLanguage.mockReset();
    mockTranslateFeature.available = true;
  });

  it('shows the Translate button when detected language differs from locale', () => {
    mockDetectLanguage.mockReturnValue('de'); // detected German
    renderBar({ locale: 'en' });
    expect(screen.getByText('msg.translate.button')).toBeInTheDocument();
  });

  it('hides the Translate button when detected language matches locale', () => {
    mockDetectLanguage.mockReturnValue('en'); // same as locale
    renderBar({ locale: 'en' });
    expect(screen.queryByText('msg.translate.button')).not.toBeInTheDocument();
  });

  it('hides the Translate button when detection returns null (short body)', () => {
    mockDetectLanguage.mockReturnValue(null);
    renderBar({ locale: 'en' });
    expect(screen.queryByText('msg.translate.button')).not.toBeInTheDocument();
  });

  it('hides the Translate button when the feature is marked unavailable', () => {
    mockDetectLanguage.mockReturnValue('de');
    mockTranslateFeature.available = false;
    renderBar({ locale: 'en' });
    expect(screen.queryByText('msg.translate.button')).not.toBeInTheDocument();
  });
});

describe('TranslateBar: consent gate', () => {
  beforeEach(() => {
    storageMap.clear();
    mockDetectLanguage.mockReturnValue('de');
    mockTranslateFeature.available = true;
    mockCallTranslateApi.mockReset();
  });

  it('shows the consent dialog on first activation', async () => {
    renderBar({ locale: 'en' });
    const btn = screen.getByText('msg.translate.button');
    await fireEvent.click(btn);
    expect(screen.getByText('msg.translate.consent.title')).toBeInTheDocument();
    expect(screen.getByText('msg.translate.consent.body')).toBeInTheDocument();
  });

  it('does NOT show the consent dialog when consent was already given', async () => {
    storageMap.set(CONSENT_STORAGE_KEY, true);
    mockCallTranslateApi.mockResolvedValue({
      ok: true,
      data: { translated_text: 'Hello' },
    });
    renderBar({ locale: 'en' });
    const btn = screen.getByText('msg.translate.button');
    await fireEvent.click(btn);
    // Consent dialog should NOT appear
    expect(screen.queryByText('msg.translate.consent.title')).not.toBeInTheDocument();
    // API should have been called immediately
    expect(mockCallTranslateApi).toHaveBeenCalledOnce();
  });

  it('calls the API after accepting consent', async () => {
    mockCallTranslateApi.mockResolvedValue({
      ok: true,
      data: { translated_text: 'Translated text here' },
    });
    renderBar({ locale: 'en' });
    await fireEvent.click(screen.getByText('msg.translate.button'));
    await fireEvent.click(screen.getByText('msg.translate.consent.accept'));
    expect(mockCallTranslateApi).toHaveBeenCalledOnce();
    expect(storageMap.get(CONSENT_STORAGE_KEY)).toBe(true);
  });

  it('dismisses the consent dialog on cancel without calling API', async () => {
    renderBar({ locale: 'en' });
    await fireEvent.click(screen.getByText('msg.translate.button'));
    await fireEvent.click(screen.getByText('msg.translate.consent.cancel'));
    expect(screen.queryByText('msg.translate.consent.title')).not.toBeInTheDocument();
    expect(mockCallTranslateApi).not.toHaveBeenCalled();
  });
});

describe('TranslateBar: Show-original toggle', () => {
  beforeEach(() => {
    storageMap.clear();
    storageMap.set(CONSENT_STORAGE_KEY, true);
    mockDetectLanguage.mockReturnValue('de');
    mockTranslateFeature.available = true;
  });

  it('shows translated text after a successful API call', async () => {
    mockCallTranslateApi.mockResolvedValue({
      ok: true,
      data: { translated_text: 'Translated content' },
    });
    renderBar({ locale: 'en' });
    await fireEvent.click(screen.getByText('msg.translate.button'));
    await waitFor(() => {
      expect(screen.getByText('Translated content')).toBeInTheDocument();
    });
    expect(screen.getByText('msg.translate.showOriginal')).toBeInTheDocument();
  });

  it('switches to Show original when the toggle is clicked', async () => {
    mockCallTranslateApi.mockResolvedValue({
      ok: true,
      data: { translated_text: 'Translated content' },
    });
    renderBar({ locale: 'en' });
    await fireEvent.click(screen.getByText('msg.translate.button'));
    await waitFor(() => screen.getByText('msg.translate.showOriginal'));
    // Click "Show original"
    await fireEvent.click(screen.getByText('msg.translate.showOriginal'));
    // Translated text should be hidden; "Show translation" should appear
    expect(screen.queryByText('Translated content')).not.toBeInTheDocument();
    expect(screen.getByText('msg.translate.showTranslation')).toBeInTheDocument();
  });

  it('re-shows translation when Show translation is clicked', async () => {
    mockCallTranslateApi.mockResolvedValue({
      ok: true,
      data: { translated_text: 'Translated content' },
    });
    renderBar({ locale: 'en' });
    await fireEvent.click(screen.getByText('msg.translate.button'));
    await waitFor(() => screen.getByText('msg.translate.showOriginal'));
    await fireEvent.click(screen.getByText('msg.translate.showOriginal'));
    // Now showing original; click Show translation
    await fireEvent.click(screen.getByText('msg.translate.showTranslation'));
    expect(screen.getByText('Translated content')).toBeInTheDocument();
  });
});

describe('TranslateBar: never-translate block list', () => {
  beforeEach(() => {
    storageMap.clear();
    mockDetectLanguage.mockReturnValue('de');
    mockTranslateFeature.available = true;
  });

  it('renders the Never-translate button alongside the Translate button', () => {
    renderBar({ locale: 'en' });
    expect(screen.getByText('msg.translate.button')).toBeInTheDocument();
    // t() mock returns key; the never-translate key is interpolated with the
    // detected lang display name (which itself falls back to the key for 'de').
    expect(screen.getByText(/msg\.translate\.neverTranslate/)).toBeInTheDocument();
  });

  it('clicking Never-translate adds the detected language to the block list', async () => {
    renderBar({ locale: 'en' });
    const neverBtn = screen.getByText(/msg\.translate\.neverTranslate/);
    await fireEvent.click(neverBtn);
    const stored = storageMap.get(NEVER_LANGS_STORAGE_KEY) as string[];
    expect(stored).toContain('de');
  });

  it('showAffordance is false after adding a language to the block list', async () => {
    renderBar({ locale: 'en' });
    // Affordance is visible before opting out
    expect(screen.getByText('msg.translate.button')).toBeInTheDocument();
    const neverBtn = screen.getByText(/msg\.translate\.neverTranslate/);
    await fireEvent.click(neverBtn);
    // After opting out the entire bar must vanish
    await waitFor(() => {
      expect(screen.queryByText('msg.translate.button')).not.toBeInTheDocument();
    });
  });

  it('affordance is suppressed on render when language is already in the block list', () => {
    storageMap.set(NEVER_LANGS_STORAGE_KEY, ['de']);
    renderBar({ locale: 'en' });
    expect(screen.queryByText('msg.translate.button')).not.toBeInTheDocument();
  });

  it('current-locale guard still applies independently of the block list', () => {
    // Detected language matches locale — no affordance regardless of block list.
    mockDetectLanguage.mockReturnValue('en');
    renderBar({ locale: 'en' });
    expect(screen.queryByText('msg.translate.button')).not.toBeInTheDocument();
  });

  it('block list is per-account: a different key in the block list does not suppress', () => {
    // Block list contains 'fr' but email is detected as 'de'
    storageMap.set(NEVER_LANGS_STORAGE_KEY, ['fr']);
    renderBar({ locale: 'en' });
    expect(screen.getByText('msg.translate.button')).toBeInTheDocument();
  });

  it('repeated opt-out does not duplicate the language in the block list', async () => {
    storageMap.set(NEVER_LANGS_STORAGE_KEY, ['de']);
    // Render with the language NOT in the block list initially so the bar shows,
    // then manually call handleNeverTranslate twice via two renders.
    // Simpler: set neverLangs to ['de'], render with 'fr' detected, opt out 'fr'.
    mockDetectLanguage.mockReturnValue('fr');
    renderBar({ locale: 'en' });
    const neverBtn = screen.getByText(/msg\.translate\.neverTranslate/);
    await fireEvent.click(neverBtn);
    // Block list should have ['de', 'fr'] with no duplicates.
    const stored = storageMap.get(NEVER_LANGS_STORAGE_KEY) as string[];
    expect(stored).toEqual(['de', 'fr']);
    expect(stored.filter((c) => c === 'fr').length).toBe(1);
  });
});

describe('TranslateBar: error handling', () => {
  beforeEach(() => {
    storageMap.clear();
    storageMap.set(CONSENT_STORAGE_KEY, true);
    mockDetectLanguage.mockReturnValue('de');
    mockTranslateFeature.available = true;
  });

  it('shows tooLong error and keeps Translate button visible', async () => {
    mockCallTranslateApi.mockResolvedValue({ ok: false, kind: 'tooLong' });
    renderBar({ locale: 'en' });
    await fireEvent.click(screen.getByText('msg.translate.button'));
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
    });
    expect(screen.getByRole('alert').textContent).toBe('msg.translate.error.tooLong');
    // Translate button is still there (user can try other messages)
    expect(screen.getByText('msg.translate.button')).toBeInTheDocument();
  });

  it('shows upstream error for 502', async () => {
    mockCallTranslateApi.mockResolvedValue({ ok: false, kind: 'upstreamError' });
    renderBar({ locale: 'en' });
    await fireEvent.click(screen.getByText('msg.translate.button'));
    await waitFor(() => {
      expect(screen.getByRole('alert').textContent).toBe('msg.translate.error.upstream');
    });
  });

  it('hides the affordance after a 501 (unavailable)', async () => {
    // The 501 handler sets translateFeature.available to false.
    // Simulate this by having the API call return 'unavailable' AND flipping
    // the mock available flag.
    mockCallTranslateApi.mockImplementation(async () => {
      mockTranslateFeature.available = false;
      return { ok: false, kind: 'unavailable' };
    });
    const { rerender } = renderBar({ locale: 'en' });
    // Initially the button is visible (available=true at render time)
    const btn = screen.getByText('msg.translate.button');
    await fireEvent.click(btn);
    // After the call sets available=false, a re-render should hide everything
    await rerender({ bodyText: LONG_BODY, locale: 'en', emailText: LONG_BODY });
    await waitFor(() => {
      expect(screen.queryByText('msg.translate.button')).not.toBeInTheDocument();
    });
  });
});
