<script module lang="ts">
  /**
   * The localStorage key under which per-account translate consent is stored.
   * Module-level export so tests can reference it without hard-coding the string.
   */
  export const CONSENT_STORAGE_KEY = 'translate.consented';

  /**
   * The localStorage key under which the per-account never-translate language
   * block list is stored. Holds a string[] of ISO 639-1 codes the user has
   * opted out of translating. Module-level export so tests can reference it.
   */
  export const NEVER_LANGS_STORAGE_KEY = 'translate.neverLangs';
</script>

<script lang="ts">
  /**
   * On-demand translation affordance for the message reading view (issue #84).
   *
   * Renders a "Translate" button when:
   *   - the translation feature is available (no previous 501 this session)
   *   - the detected body language differs from the active UI locale
   *   - the body is long enough for reliable detection
   *
   * On first activation shows a one-time consent notice; persists consent in
   * localStorage via accountKey so the notice never appears again for the
   * same account. Calls POST /api/v1/translate on the same-origin backend
   * (never contacts the external service directly). Translated text is rendered
   * as a plain-text overlay; "Show original" and "Show translation" toggle.
   *
   * Feature-unavailable (501) disables the affordance for the entire session
   * without surfacing an error to the user.
   */
  import { t } from '../i18n/i18n.svelte';
  import { translateFeature, callTranslateApi } from '../translate/translate-api.svelte';
  import { detectLanguage } from '../translate/lang-map';
  import { readAccountJson, writeAccountJson } from '../storage/account-scoped';

  interface Props {
    /** Plain text extracted from the message body; used for language detection. */
    bodyText: string;
    /** Active UI locale base, e.g. 'en' or 'de'. */
    locale: string;
    /** Plain text to send to the translate API. */
    emailText: string;
  }

  let { bodyText, locale, emailText }: Props = $props();

  // ── Language detection ────────────────────────────────────────────────────
  let detectedLang = $derived(detectLanguage(bodyText));

  // ── Never-translate block list ────────────────────────────────────────────
  // Per-account list of ISO 639-1 codes the user has opted out of translating.
  let neverLangs = $state<string[]>([]);

  // Hydrate block list from localStorage on mount (re-runs on auth change so
  // the list stays correct when switching accounts).
  $effect(() => {
    neverLangs = readAccountJson<string[]>(NEVER_LANGS_STORAGE_KEY, []);
  });

  /**
   * True when the Translate button should be visible:
   *   - feature available (no 501 this session)
   *   - body is long enough and in a known language
   *   - detected language differs from the active locale
   *   - detected language is not in the user's never-translate block list
   */
  let showAffordance = $derived(
    translateFeature.available &&
      detectedLang !== null &&
      detectedLang !== locale &&
      !neverLangs.includes(detectedLang),
  );

  // ── Consent state ─────────────────────────────────────────────────────────
  let consentGiven = $state(false);
  let consentDialogOpen = $state(false);

  // Hydrate consent from localStorage on mount. The CONSENT_STORAGE_KEY
  // value is account-scoped via readAccountJson, so consent is per-account.
  $effect(() => {
    consentGiven = readAccountJson<boolean>(CONSENT_STORAGE_KEY, false);
  });

  // ── Translation state ─────────────────────────────────────────────────────
  let translating = $state(false);
  let translatedText = $state<string | null>(null);
  let showingTranslation = $state(false);
  let errorKey = $state<string | null>(null);

  // ── Helpers ───────────────────────────────────────────────────────────────

  /**
   * Return the display name for an ISO 639-1 code using the existing
   * `settings.language.{code}` i18n entries. Falls back to the raw code
   * for languages not yet in the catalogue (e.g. 'fr' -> 'fr').
   */
  function langDisplayName(code: string): string {
    const key = `settings.language.${code}`;
    const name = t(key);
    // t() returns the key itself when no translation exists; use the bare
    // ISO code as the fallback in that case.
    return name === key ? code : name;
  }

  // ── Actions ───────────────────────────────────────────────────────────────

  function handleTranslateClick(): void {
    if (consentGiven) {
      void doTranslate();
    } else {
      consentDialogOpen = true;
    }
  }

  function handleConsentAccept(): void {
    consentGiven = true;
    writeAccountJson(CONSENT_STORAGE_KEY, true);
    consentDialogOpen = false;
    void doTranslate();
  }

  function handleConsentCancel(): void {
    consentDialogOpen = false;
  }

  async function doTranslate(): Promise<void> {
    errorKey = null;
    translating = true;
    const result = await callTranslateApi({
      text: emailText,
      target_lang: locale,
      source_lang: detectedLang ?? undefined,
    });
    translating = false;

    if (!result.ok) {
      switch (result.kind) {
        case 'unavailable':
          // 501: feature turned off this session; showAffordance reacts to
          // translateFeature.available going false and hides automatically.
          break;
        case 'tooLong':
          errorKey = 'msg.translate.error.tooLong';
          break;
        case 'upstreamError':
          errorKey = 'msg.translate.error.upstream';
          break;
        case 'badRequest':
          errorKey = 'msg.translate.error.badRequest';
          break;
        default:
          errorKey = 'msg.translate.error.network';
          break;
      }
      return;
    }

    translatedText = result.data.translated_text;
    showingTranslation = true;
  }

  function handleShowOriginal(): void {
    showingTranslation = false;
  }

  function handleShowTranslation(): void {
    showingTranslation = true;
  }

  /**
   * Add the currently detected language to the user's never-translate block
   * list and persist it. showAffordance reacts immediately so the bar hides.
   */
  function handleNeverTranslate(): void {
    if (detectedLang === null) return;
    const lang = detectedLang;
    const updated = neverLangs.includes(lang) ? neverLangs : [...neverLangs, lang];
    neverLangs = updated;
    writeAccountJson(NEVER_LANGS_STORAGE_KEY, updated);
  }
</script>

{#if showAffordance}
  <div class="translate-bar" role="region" aria-label="Translation">
    {#if !showingTranslation || translatedText === null}
      <!-- Translate button or in-progress indicator -->
      {#if translating}
        <span class="translating" role="status">{t('msg.translate.translating')}</span>
      {:else}
        <button
          type="button"
          class="translate-btn"
          onclick={handleTranslateClick}
          disabled={translating}
        >
          {t('msg.translate.button')}
        </button>
      {/if}
      <!-- Per-language opt-out: permanently suppresses the affordance for
           the detected source language across all messages. showAffordance
           already ensures detectedLang is non-null here; the ?? '' guards
           TypeScript since it cannot narrow through the {#if showAffordance}
           block. -->
      <button type="button" class="never-btn" onclick={handleNeverTranslate}>
        {t('msg.translate.neverTranslate', { lang: langDisplayName(detectedLang ?? '') })}
      </button>
      {#if translatedText !== null}
        <!-- Already have a translation; offer to show it again -->
        <button type="button" class="toggle-btn" onclick={handleShowTranslation}>
          {t('msg.translate.showTranslation')}
        </button>
      {/if}
    {:else}
      <!-- Showing translation: offer to go back to original -->
      <button type="button" class="toggle-btn" onclick={handleShowOriginal}>
        {t('msg.translate.showOriginal')}
      </button>
    {/if}

    {#if errorKey}
      <span class="error" role="alert">{t(errorKey)}</span>
    {/if}
  </div>

  <!-- First-use consent dialog (inline, non-modal) -->
  {#if consentDialogOpen}
    <div class="consent-notice" role="dialog" aria-modal="true" aria-labelledby="consent-title">
      <p id="consent-title" class="consent-title">{t('msg.translate.consent.title')}</p>
      <p class="consent-body">{t('msg.translate.consent.body')}</p>
      <div class="consent-actions">
        <button type="button" class="consent-accept" onclick={handleConsentAccept}>
          {t('msg.translate.consent.accept')}
        </button>
        <button type="button" class="consent-cancel" onclick={handleConsentCancel}>
          {t('msg.translate.consent.cancel')}
        </button>
      </div>
    </div>
  {/if}

  <!-- Translated body overlay (plain text) -->
  {#if showingTranslation && translatedText !== null}
    <pre class="translated-body">{translatedText}</pre>
  {/if}
{/if}

<style>
  .translate-bar {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--spacing-03);
    padding: var(--spacing-02) var(--spacing-04);
    margin-bottom: var(--spacing-03);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    font-size: var(--type-body-compact-01-size);
  }

  .translate-btn {
    color: var(--interactive);
    font-weight: 600;
    padding: var(--spacing-01) var(--spacing-03);
    border-radius: var(--radius-md);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .translate-btn:hover:not(:disabled) {
    background: var(--layer-02);
  }
  .translate-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .toggle-btn {
    color: var(--text-secondary);
    padding: var(--spacing-01) var(--spacing-03);
    border-radius: var(--radius-md);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    font-size: var(--type-body-compact-01-size);
  }
  .toggle-btn:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .never-btn {
    color: var(--text-helper);
    padding: var(--spacing-01) var(--spacing-03);
    border-radius: var(--radius-md);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    font-size: var(--type-body-compact-01-size);
  }
  .never-btn:hover {
    background: var(--layer-02);
    color: var(--text-secondary);
  }

  .translating {
    color: var(--text-helper);
  }

  .error {
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
  }

  .consent-notice {
    margin-bottom: var(--spacing-03);
    padding: var(--spacing-04);
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
  }

  .consent-title {
    font-weight: 600;
    color: var(--text-primary);
    margin: 0 0 var(--spacing-02) 0;
  }

  .consent-body {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    margin: 0 0 var(--spacing-04) 0;
  }

  .consent-actions {
    display: flex;
    gap: var(--spacing-03);
  }

  .consent-accept {
    background: var(--interactive);
    color: var(--text-on-color);
    font-weight: 600;
    padding: var(--spacing-02) var(--spacing-05);
    border-radius: var(--radius-pill);
    transition: opacity var(--duration-fast-02) var(--easing-productive-enter);
  }
  .consent-accept:hover {
    opacity: 0.9;
  }

  .consent-cancel {
    color: var(--text-secondary);
    padding: var(--spacing-02) var(--spacing-05);
    border-radius: var(--radius-pill);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .consent-cancel:hover {
    background: var(--layer-03);
  }

  .translated-body {
    margin: 0 0 var(--spacing-04) 0;
    padding: var(--spacing-04);
    background: var(--layer-01);
    border-radius: var(--radius-md);
    white-space: pre-wrap;
    word-break: break-word;
    font-family: var(--font-sans);
    font-size: var(--type-body-01-size);
    color: var(--text-primary);
  }

  @media print {
    .translate-bar,
    .consent-notice {
      display: none !important;
    }
  }
</style>
