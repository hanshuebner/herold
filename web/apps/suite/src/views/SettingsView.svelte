<script lang="ts">
  /**
   * Settings panel per docs/requirements/20-settings.md.
   *
   * Section-driven layout with a left-side nav. Sections map to either
   * local-only state (settings store, REQ-SET-01..06/12..13) or
   * server-side state (Identity / future VacationResponse — REQ-SET-02..03).
   * The "About" section reads from the live JMAP session resource so the
   * operator can verify which server they're talking to (REQ-SET-22).
   */
  import { settings } from '../lib/settings/settings.svelte';
  import { auth } from '../lib/auth/auth.svelte';
  import { mail } from '../lib/mail/store.svelte';
  import { router } from '../lib/router/router.svelte';
  import IdentitySignatureForm from './settings/IdentitySignatureForm.svelte';
  import SecurityForm from './settings/SecurityForm.svelte';
  import ApiKeysForm from './settings/ApiKeysForm.svelte';
  import VacationForm from './settings/VacationForm.svelte';
  import SieveForm from './settings/SieveForm.svelte';
  import CategoriesForm from './settings/CategoriesForm.svelte';
  import FiltersForm from './settings/FiltersForm.svelte';
  import { Capability } from '../lib/jmap/types';
  import { jmap } from '../lib/jmap/client';
  import { LOCALES, type Locale } from '../lib/i18n/i18n.svelte';
  import { t } from '../lib/i18n/i18n.svelte';

  // Section order per Phase 4 spec: Account, Security, Appearance, Mail,
  // Categories, Filters, API keys, Privacy, About.
  type Section = 'account' | 'security' | 'appearance' | 'mail' | 'categories' | 'filters' | 'api-keys' | 'privacy' | 'about';

  let hasCategorise = $derived(jmap.hasCapability(Capability.HeroldCategorise));
  let hasManagedRules = $derived(jmap.hasCapability(Capability.HeroldManagedRules));

  let sectionsBase: { id: Section; label: string }[] = [
    { id: 'account', label: 'Account' },
    { id: 'security', label: 'Security' },
    { id: 'appearance', label: 'Appearance' },
    { id: 'mail', label: 'Mail' },
    { id: 'api-keys', label: 'API keys' },
    { id: 'privacy', label: 'Privacy' },
    { id: 'about', label: 'About' },
  ];

  let SECTIONS = $derived.by(() => {
    const result: { id: Section; label: string }[] = [];
    for (const s of sectionsBase) {
      result.push(s);
      if (s.id === 'mail') {
        if (hasCategorise) result.push({ id: 'categories', label: 'Categories' });
        if (hasManagedRules) result.push({ id: 'filters', label: 'Filters' });
      }
    }
    return result;
  });

  // The active section comes from /settings/<section>; default = account.
  let activeSection = $derived<Section>(
    (router.parts[1] as Section | undefined) &&
      SECTIONS.some((s) => s.id === router.parts[1])
      ? (router.parts[1] as Section)
      : 'account',
  );

  function selectSection(id: Section): void {
    router.navigate(`/settings/${id}`);
  }

  let identitiesArray = $derived(Array.from(mail.identities.values()));

  // Lazy-load identities when the user opens settings — they are needed
  // for Account section and may not have been fetched yet (e.g. straight
  // navigation to /settings without loading the inbox first).
  $effect(() => {
    if (mail.identities.size === 0) void mail.loadIdentities();
  });

  // ── Helpers / labels for the form rows ────────────────────────────────
  const SWIPE_OPTIONS = [
    { value: 'archive', label: 'Archive' },
    { value: 'snooze', label: 'Snooze' },
    { value: 'delete', label: 'Delete' },
    { value: 'mark_read', label: 'Mark read' },
    { value: 'label', label: 'Label…' },
    { value: 'none', label: 'None' },
  ] as const;

  let capabilityList = $derived(
    auth.session ? Object.keys(auth.session.capabilities).sort() : [],
  );

  const APP_VERSION: string =
    typeof __HEROLD_VERSION__ !== 'undefined' ? __HEROLD_VERSION__ : 'dev';
</script>

<div class="settings-shell">
  <nav class="side-nav" aria-label="Settings sections">
    <h1>Settings</h1>
    <ul>
      {#each SECTIONS as section (section.id)}
        <li>
          <button
            type="button"
            class:active={activeSection === section.id}
            onclick={() => selectSection(section.id)}
          >
            {section.label}
          </button>
        </li>
      {/each}
    </ul>
  </nav>

  <section class="content" aria-label={SECTIONS.find((s) => s.id === activeSection)?.label}>
    {#if activeSection === 'account'}
      <h2>Account</h2>

      <div class="row">
        <span class="label">Signed in as</span>
        <span class="value">{auth.session?.username ?? '—'}</span>
      </div>

      <h3>Identities &amp; signatures</h3>
      {#if identitiesArray.length === 0}
        <p class="muted">No identities loaded yet.</p>
      {:else}
        {#each identitiesArray as identity (identity.id)}
          <IdentitySignatureForm {identity} />
        {/each}
      {/if}

    {:else if activeSection === 'security'}
      <h2>Security</h2>
      <SecurityForm />

    {:else if activeSection === 'appearance'}
      <h2>{t('settings.appearance')}</h2>

      <div class="row vertical">
        <span class="label">{t('settings.theme')}</span>
        <div class="segmented" role="radiogroup" aria-label={t('settings.theme')}>
          {#each ['system', 'light', 'dark'] as const as choice}
            <button
              type="button"
              role="radio"
              aria-checked={settings.theme === choice}
              class:on={settings.theme === choice}
              onclick={() => settings.setTheme(choice)}
            >
              {t(`settings.theme.${choice}`)}
            </button>
          {/each}
        </div>
        <p class="hint">
          System follows your OS-level preference and updates live when you toggle it.
        </p>
      </div>

      <div class="row vertical">
        <span class="label">{t('settings.language')}</span>
        <div class="segmented" role="radiogroup" aria-label={t('settings.language')}>
          {#each LOCALES as locale}
            <button
              type="button"
              role="radio"
              aria-checked={settings.locale === locale}
              class:on={settings.locale === locale}
              onclick={() => settings.setLocale(locale as Locale)}
            >
              {t(`settings.language.${locale}`)}
            </button>
          {/each}
        </div>
      </div>

    {:else if activeSection === 'mail'}
      <h2>Mail</h2>

      <div class="row vertical">
        <span class="label">Undo-send window</span>
        <div class="undo">
          <input
            type="range"
            min="0"
            max="30"
            step="1"
            value={settings.undoWindowSec}
            oninput={(e) =>
              settings.setUndoWindowSec(
                parseInt((e.currentTarget as HTMLInputElement).value, 10),
              )}
            aria-label="Seconds before send"
          />
          <span class="undo-value">
            {settings.undoWindowSec === 0
              ? 'Off (sends immediately)'
              : `${settings.undoWindowSec}s`}
          </span>
        </div>
        <p class="hint">
          When set, sends are held server-side; the toast's Undo cancels delivery.
        </p>
      </div>

      <div class="row vertical">
        <span class="label">Swipe-left action <span class="muted">(touch)</span></span>
        <select
          value={settings.swipeLeft}
          onchange={(e) =>
            settings.setSwipeLeft(
              (e.currentTarget as HTMLSelectElement).value as
                | 'archive'
                | 'snooze'
                | 'delete'
                | 'mark_read'
                | 'label'
                | 'none',
            )}
        >
          {#each SWIPE_OPTIONS as opt (opt.value)}
            <option value={opt.value}>{opt.label}</option>
          {/each}
        </select>
      </div>

      <div class="row vertical">
        <span class="label">Swipe-right action <span class="muted">(touch)</span></span>
        <select
          value={settings.swipeRight}
          onchange={(e) =>
            settings.setSwipeRight(
              (e.currentTarget as HTMLSelectElement).value as
                | 'archive'
                | 'snooze'
                | 'delete'
                | 'mark_read'
                | 'label'
                | 'none',
            )}
        >
          {#each SWIPE_OPTIONS as opt (opt.value)}
            <option value={opt.value}>{opt.label}</option>
          {/each}
        </select>
      </div>

      <h3>Vacation auto-reply</h3>
      <VacationForm />

      <h3>Sieve filtering</h3>
      <SieveForm />

      <h3>Shortcut coach</h3>
      <div class="row">
        <span class="label">Show coach hints</span>
        <label class="switch">
          <input
            type="checkbox"
            checked={settings.coachEnabled}
            onchange={(e) =>
              settings.setCoachEnabled((e.currentTarget as HTMLInputElement).checked)}
          />
          <span class="track" aria-hidden="true"></span>
        </label>
      </div>

    {:else if activeSection === 'categories'}
      <h2>Categories</h2>
      <CategoriesForm />

    {:else if activeSection === 'filters'}
      <h2>Filters</h2>
      <FiltersForm />

    {:else if activeSection === 'api-keys'}
      <h2>API keys</h2>
      <ApiKeysForm />

    {:else if activeSection === 'privacy'}
      <h2>Privacy</h2>

      <div class="row vertical">
        <span class="label">External images</span>
        <div class="segmented" role="radiogroup" aria-label="External-image loading">
          {#each ['never', 'per-sender', 'always'] as const as choice}
            <button
              type="button"
              role="radio"
              aria-checked={settings.imageLoadDefault === choice}
              class:on={settings.imageLoadDefault === choice}
              onclick={() => settings.setImageLoadDefault(choice)}
            >
              {choice === 'never'
                ? 'Never'
                : choice === 'per-sender'
                  ? 'Per sender'
                  : 'Always'}
            </button>
          {/each}
        </div>
        <p class="hint">
          External images can act as read receipts. <em>Never</em> blocks them by default;
          <em>Per sender</em> only loads from senders you've allowed.
        </p>
      </div>

      {#if settings.imageLoadDefault === 'per-sender'}
        <h3>Allowed senders</h3>
        {#if settings.imageAllowList.length === 0}
          <p class="muted">No senders allowed yet. Use "Always from &lt;sender&gt;" in
            the reading pane to add one.</p>
        {:else}
          <ul class="list">
            {#each settings.imageAllowList as sender (sender)}
              <li>
                <span class="value">{sender}</span>
                <button
                  type="button"
                  class="link"
                  onclick={() => settings.removeImageAllowedSender(sender)}
                >
                  Remove
                </button>
              </li>
            {/each}
          </ul>
        {/if}
      {/if}

    {:else if activeSection === 'about'}
      <h2>About</h2>
      <div class="row">
        <span class="label">Herold version</span>
        <span class="value mono">{APP_VERSION}</span>
      </div>
      <div class="row">
        <span class="label">JMAP API URL</span>
        <span class="value mono">{auth.session?.apiUrl ?? '—'}</span>
      </div>
      <div class="row">
        <span class="label">EventSource URL</span>
        <span class="value mono">{auth.session?.eventSourceUrl ?? '—'}</span>
      </div>
      <div class="row">
        <span class="label">Session state</span>
        <span class="value mono">{auth.session?.state ?? '—'}</span>
      </div>

      <h3>Server capabilities</h3>
      {#if capabilityList.length === 0}
        <p class="muted">No session.</p>
      {:else}
        <ul class="caps">
          {#each capabilityList as cap (cap)}
            <li class="mono">{cap}</li>
          {/each}
        </ul>
      {/if}
    {/if}
  </section>
</div>

<style>
  .settings-shell {
    display: grid;
    grid-template-columns: 220px 1fr;
    gap: var(--spacing-06);
    padding: var(--spacing-06) var(--spacing-07);
    height: 100%;
    overflow: auto;
  }

  .side-nav h1 {
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    font-weight: var(--type-heading-03-weight);
    margin: 0 0 var(--spacing-05);
    color: var(--text-primary);
  }
  .side-nav ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }
  .side-nav button {
    width: 100%;
    text-align: left;
    padding: var(--spacing-03) var(--spacing-04);
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .side-nav button:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .side-nav button.active {
    background: var(--layer-02);
    color: var(--text-primary);
    font-weight: 600;
  }

  .content {
    max-width: 720px;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }
  .content h2 {
    font-size: var(--type-heading-02-size);
    line-height: var(--type-heading-02-line);
    font-weight: var(--type-heading-02-weight);
    margin: 0 0 var(--spacing-04);
  }
  .content h3 {
    font-size: var(--type-heading-compact-02-size);
    line-height: var(--type-heading-compact-02-line);
    font-weight: var(--type-heading-compact-02-weight);
    margin: var(--spacing-05) 0 var(--spacing-02);
    color: var(--text-secondary);
  }

  .row {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    padding: var(--spacing-03) 0;
    border-bottom: 1px solid var(--border-subtle-01);
  }
  .row.vertical {
    flex-direction: column;
    align-items: stretch;
    gap: var(--spacing-02);
  }
  .label {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    flex: 0 0 auto;
    min-width: 12em;
  }
  .row.vertical .label {
    min-width: 0;
  }
  .value {
    color: var(--text-primary);
    font-size: var(--type-body-01-size);
    flex: 1;
    word-break: break-all;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
  }
  .muted {
    color: var(--text-helper);
    font-style: italic;
  }
  .hint {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  .segmented {
    display: inline-flex;
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    overflow: hidden;
    width: fit-content;
  }
  .segmented button {
    padding: var(--spacing-02) var(--spacing-04);
    color: var(--text-secondary);
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .segmented button:not(:last-child) {
    border-right: 1px solid var(--border-subtle-01);
  }
  .segmented button:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .segmented button.on {
    background: var(--interactive);
    color: var(--text-on-color);
  }

  .undo {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
  }
  .undo input[type='range'] {
    flex: 1;
    accent-color: var(--interactive);
  }
  .undo-value {
    font-variant-numeric: tabular-nums;
    color: var(--text-primary);
    min-width: 12ch;
  }

  select {
    background: var(--layer-01);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-02) var(--spacing-03);
    min-height: var(--touch-min);
    width: max-content;
  }

  .switch {
    position: relative;
    display: inline-flex;
    width: 44px;
    height: 24px;
    cursor: pointer;
  }
  .switch input {
    position: absolute;
    inset: 0;
    opacity: 0;
    width: 100%;
    height: 100%;
    margin: 0;
    cursor: pointer;
  }
  .switch .track {
    width: 100%;
    height: 100%;
    background: var(--layer-02);
    border-radius: var(--radius-pill);
    position: relative;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .switch .track::before {
    content: '';
    position: absolute;
    top: 2px;
    left: 2px;
    width: 20px;
    height: 20px;
    background: var(--text-on-color);
    border-radius: var(--radius-pill);
    transition: transform var(--duration-fast-02) var(--easing-productive-enter);
  }
  .switch input:checked + .track {
    background: var(--interactive);
  }
  .switch input:checked + .track::before {
    transform: translateX(20px);
  }

  .list,
  .caps {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }
  .list li {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--layer-01);
    border-radius: var(--radius-md);
  }
  .caps li {
    padding: var(--spacing-02) var(--spacing-03);
    color: var(--text-secondary);
    background: var(--layer-01);
    border-radius: var(--radius-sm);
    word-break: break-all;
  }

  .link {
    color: var(--interactive);
    font-weight: 500;
  }
  .link:hover {
    text-decoration: underline;
  }

  @media (max-width: 768px) {
    .settings-shell {
      grid-template-columns: 1fr;
      padding: var(--spacing-04);
    }
  }
</style>
