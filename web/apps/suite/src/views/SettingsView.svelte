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
  import IdentityDisplayNameForm from './settings/IdentityDisplayNameForm.svelte';
  import IdentityAvatarForm from './settings/IdentityAvatarForm.svelte';
  import IdentityEditDialog from './settings/IdentityEditDialog.svelte';
  import SecurityForm from './settings/SecurityForm.svelte';
  import ApiKeysForm from './settings/ApiKeysForm.svelte';
  import VacationForm from './settings/VacationForm.svelte';
  import SieveForm from './settings/SieveForm.svelte';
  import CategoriesForm from './settings/CategoriesForm.svelte';
  import FiltersForm from './settings/FiltersForm.svelte';
  import PrivacyForm from './settings/PrivacyForm.svelte';
  import DiagnosticsForm from './settings/DiagnosticsForm.svelte';
  import { Capability } from '../lib/jmap/types';
  import { jmap } from '../lib/jmap/client';
  import { LOCALES, type Locale } from '../lib/i18n/i18n.svelte';
  import { t } from '../lib/i18n/i18n.svelte';
  import { llmTransparency } from '../lib/llm/transparency.svelte';
  import { pushSubscription } from '../lib/push/push-subscription.svelte';
  import { sounds } from '../lib/notifications/sounds.svelte';
  import { hasExternalSubmission } from '../lib/auth/capabilities';
  import { submissionStore } from '../lib/identities/identity-submission.svelte';
  import type { Identity } from '../lib/mail/types';

  // Hydrate the sounds toggle from localStorage on mount.
  sounds.hydrate();

  // Section order: Account, Security, Appearance, Mail, Categories, Filters,
  // Notifications, API keys, Privacy, Diagnostics, About.
  type Section =
    | 'account'
    | 'security'
    | 'appearance'
    | 'mail'
    | 'categories'
    | 'filters'
    | 'notifications'
    | 'api-keys'
    | 'privacy'
    | 'diagnostics'
    | 'about';

  let hasCategorise = $derived(jmap.hasCapability(Capability.HeroldCategorise));
  let hasManagedRules = $derived(jmap.hasCapability(Capability.HeroldManagedRules));
  let hasLLMTransparency = $derived(jmap.hasCapability(Capability.HeroldLLMTransparency));
  let hasPush = $derived(jmap.hasCapability(Capability.HeroldPush));

  let SECTIONS = $derived.by<{ id: Section; label: string }[]>(() => {
    const result: { id: Section; label: string }[] = [
      { id: 'account', label: t('settings.account') },
      { id: 'security', label: t('settings.security') },
      { id: 'appearance', label: t('settings.appearance') },
      { id: 'mail', label: t('settings.mail') },
    ];
    if (hasCategorise) result.push({ id: 'categories', label: t('settings.categories.heading') });
    if (hasManagedRules) result.push({ id: 'filters', label: t('settings.filters.heading') });
    // Notifications section always shown (in-app sounds; push if available).
    result.push({ id: 'notifications', label: t('settings.notifications') });
    result.push({ id: 'api-keys', label: t('settings.apiKeys') });
    result.push({ id: 'privacy', label: t('settings.privacy.heading') });
    result.push({ id: 'diagnostics', label: t('settings.diagnostics.heading') });
    result.push({ id: 'about', label: t('settings.about') });
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

  // ── Identity edit dialog (external submission) ──────────────────────────

  let showExtSub = $derived(hasExternalSubmission());

  /** The identity currently open in the edit dialog, or null when closed. */
  let editDialogIdentity = $state<Identity | null>(null);
  /** Whether the edit dialog should scroll to the submission section on open. */
  let editDialogScrollToSubmission = $state(false);

  function openEditDialog(identity: Identity, scrollToSubmission = false): void {
    editDialogIdentity = identity;
    editDialogScrollToSubmission = scrollToSubmission;
    // Pre-load the submission status for this identity.
    if (showExtSub) {
      void submissionStore.forIdentity(identity.id).load();
    }
  }

  function closeEditDialog(): void {
    editDialogIdentity = null;
    editDialogScrollToSubmission = false;
  }

  // Handle the ?identity=<id>&action=reauth route param set by the
  // compose failure toast's "Re-authenticate" button.
  $effect(() => {
    const identityParam = router.getParam('identity');
    const actionParam = router.getParam('action');
    if (
      identityParam &&
      actionParam === 'reauth' &&
      activeSection === 'account' &&
      showExtSub
    ) {
      const identity = mail.identities.get(identityParam);
      if (identity && editDialogIdentity?.id !== identityParam) {
        openEditDialog(identity, true);
        // Clear the params from the URL so a back-navigation does not
        // re-open the dialog.
        router.setParam('identity', null);
        router.setParam('action', null);
      }
    }
  });

  // Load submission statuses for all identities when the Account section is
  // open and the external submission capability is present, so badges render
  // without a per-row lazy load.
  $effect(() => {
    if (activeSection === 'account' && showExtSub) {
      for (const identity of mail.identities.values()) {
        void submissionStore.forIdentity(identity.id).load();
      }
    }
  });

  // Lazy-load LLM transparency data when the user opens settings and the
  // capability is present. Needed for the Spam section disclosure.
  $effect(() => {
    if (hasLLMTransparency && llmTransparency.loadStatus === 'idle') {
      void llmTransparency.load();
    }
  });

  // ── Helpers / labels for the form rows ────────────────────────────────
  let SWIPE_OPTIONS = $derived([
    { value: 'archive', label: t('settings.mail.swipe.archive') },
    { value: 'snooze', label: t('settings.mail.swipe.snooze') },
    { value: 'delete', label: t('settings.mail.swipe.delete') },
    { value: 'mark_read', label: t('settings.mail.swipe.markRead') },
    { value: 'label', label: t('settings.mail.swipe.label') },
    { value: 'none', label: t('settings.mail.swipe.none') },
  ] as const);

  let capabilityList = $derived(
    auth.session ? Object.keys(auth.session.capabilities).sort() : [],
  );

  const APP_VERSION: string =
    typeof __HEROLD_VERSION__ !== 'undefined' ? __HEROLD_VERSION__ : 'dev';
</script>

<div class="settings-shell">
  <nav class="side-nav" aria-label={t('settings.sectionsAria')}>
    <h1>{t('settings.title')}</h1>
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
      <h2>{t('settings.account')}</h2>

      <div class="row">
        <span class="label">{t('settings.account.signedInAs')}</span>
        <span class="value">{auth.session?.username ?? '—'}</span>
        <button
          type="button"
          class="signout-btn"
          onclick={() => void auth.logout()}
        >
          {t('settings.account.signOut')}
        </button>
      </div>

      <h3>{t('settings.account.identitiesHeading')}</h3>
      {#if identitiesArray.length === 0}
        <p class="muted">{t('settings.account.noIdentities')}</p>
      {:else}
        {#each identitiesArray as identity (identity.id)}
          {#if showExtSub}
            {@const subHandle = submissionStore.forIdentity(identity.id)}
            {@const subData = subHandle.data}
            {@const subState = subData?.state}
            {@const isAlert = subState === 'auth-failed' || subState === 'unreachable'}
            <div class="identity-row-wrapper">
              <div class="identity-badge-row">
                <span class="identity-label">
                  {identity.name ? `${identity.name} <${identity.email}>` : identity.email}
                </span>
                {#if subData?.configured}
                  {#if isAlert}
                    <button
                      type="button"
                      class="badge badge-alert"
                      onclick={() => openEditDialog(identity, true)}
                      title={subState === 'auth-failed'
                        ? t('settings.account.authFailedTitle')
                        : t('settings.account.unreachableTitle')}
                    >
                      {subState === 'auth-failed'
                        ? t('settings.account.authFailedBadge')
                        : t('settings.account.unreachableBadge')}
                    </button>
                  {:else}
                    <button
                      type="button"
                      class="badge badge-external"
                      onclick={() => openEditDialog(identity, true)}
                      title={t('settings.account.externalBadgeTitle')}
                    >
                      {t('settings.account.externalBadge')}
                    </button>
                  {/if}
                {:else}
                  <button
                    type="button"
                    class="badge-link"
                    onclick={() => openEditDialog(identity, true)}
                    title={t('settings.account.configureExternalTitle')}
                  >
                    {t('settings.account.configureExternal')}
                  </button>
                {/if}
              </div>
              <IdentityAvatarForm {identity} />
              <IdentityDisplayNameForm {identity} />
              <IdentitySignatureForm {identity} />
            </div>
          {:else}
            <IdentityAvatarForm {identity} />
            <IdentityDisplayNameForm {identity} />
            <IdentitySignatureForm {identity} />
          {/if}
        {/each}
        {#if !showExtSub}
          <p class="hint ext-sub-hint">
            {@html t('settings.account.extSubHint', {
              systemToml: '<code>system.toml</code>',
              docPath: '<code>docs/manual/admin/external-smtp-submission.mdoc</code>',
            })}
          </p>
        {/if}
      {/if}

      {#if editDialogIdentity}
        <IdentityEditDialog
          identity={editDialogIdentity}
          onclose={closeEditDialog}
          scrollToSubmission={editDialogScrollToSubmission}
        />
      {/if}

    {:else if activeSection === 'security'}
      <h2>{t('settings.security')}</h2>
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
          {t('settings.appearance.themeHint')}
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
      <h2>{t('settings.mail')}</h2>

      <div class="row vertical">
        <span class="label">{t('settings.mail.undoSendWindow')}</span>
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
            aria-label={t('settings.mail.undoSendAria')}
          />
          <span class="undo-value">
            {settings.undoWindowSec === 0
              ? t('settings.mail.undoSendOff')
              : t('settings.mail.undoSendValue', { seconds: settings.undoWindowSec })}
          </span>
        </div>
        <p class="hint">
          {t('settings.mail.undoSendHint')}
        </p>
      </div>

      <div class="row vertical">
        <span class="label">{t('settings.mail.swipeLeft')} <span class="muted">{t('settings.mail.swipeTouch')}</span></span>
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
        <span class="label">{t('settings.mail.swipeRight')} <span class="muted">{t('settings.mail.swipeTouch')}</span></span>
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

      <h3>{t('settings.mail.vacationHeading')}</h3>
      <VacationForm />

      <h3>{t('settings.mail.sieveHeading')}</h3>
      <SieveForm />

      {#if hasLLMTransparency}
        <h3>{t('settings.mail.spamHeading')}</h3>
        <p class="hint">
          {t('settings.mail.spamHint')}
        </p>
        {#if llmTransparency.loadStatus === 'loading' || llmTransparency.loadStatus === 'idle'}
          <p class="muted">{t('settings.mail.spamLoading')}</p>
        {:else if llmTransparency.loadStatus === 'error'}
          <p class="muted">{llmTransparency.loadError ?? t('settings.mail.spamLoadError')}</p>
        {:else if llmTransparency.data?.spamPrompt}
          <pre class="prompt-display">{llmTransparency.data.spamPrompt}</pre>
          {#if llmTransparency.data.spamModel}
            <div class="row">
              <span class="label">{t('settings.mail.spamModelLabel')}</span>
              <span class="value mono">{llmTransparency.data.spamModel}</span>
            </div>
          {/if}
          {#if llmTransparency.data.disclosureNote}
            <div class="disclosure-note">
              <p>{llmTransparency.data.disclosureNote}</p>
            </div>
          {/if}
        {:else}
          <p class="muted">{t('settings.mail.spamNoPrompt')}</p>
        {/if}
      {/if}

      <h3>{t('settings.mail.coachHeading')}</h3>
      <div class="row">
        <span class="label">{t('settings.mail.coachLabel')}</span>
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
      <h2>{t('settings.categories.heading')}</h2>
      <CategoriesForm />

    {:else if activeSection === 'filters'}
      <h2>{t('settings.filters.heading')}</h2>
      <FiltersForm />

    {:else if activeSection === 'notifications'}
      <h2>{t('settings.notifications')}</h2>

      <div class="row vertical">
        <span class="label">{t('settings.notifications.soundsLabel')}</span>
        <p class="hint">
          {t('settings.notifications.soundsHint')}
        </p>
        <label class="switch" aria-label={t('settings.notifications.soundsAria')}>
          <input
            type="checkbox"
            checked={sounds.enabled}
            onchange={(e) => sounds.setEnabled((e.currentTarget as HTMLInputElement).checked)}
          />
          <span class="track" aria-hidden="true"></span>
        </label>
      </div>

      {#if hasPush}
        <div class="row vertical">
          <span class="label">{t('settings.notifications.pushLabel')}</span>
          {#if pushSubscription.permissionState === 'denied'}
            <p class="hint">
              {t('settings.notifications.pushDeniedHint')}
            </p>
            <button
              type="button"
              onclick={() => pushSubscription.forgetDenial()}
            >
              {t('settings.notifications.pushForget')}
            </button>
          {:else if pushSubscription.subscribed}
            <p class="hint">{t('settings.notifications.pushOnHint')}</p>
            <button
              type="button"
              onclick={() => void pushSubscription.unsubscribe()}
              disabled={pushSubscription.busy}
            >
              {pushSubscription.busy
                ? t('settings.notifications.pushUpdating')
                : t('settings.notifications.pushDisable')}
            </button>
          {:else}
            <p class="hint">
              {t('settings.notifications.pushOffHint')}
            </p>
            <button
              type="button"
              class="primary-action"
              onclick={() => void pushSubscription.subscribe()}
              disabled={pushSubscription.busy}
            >
              {pushSubscription.busy
                ? t('settings.notifications.pushEnabling')
                : t('settings.notifications.pushEnable')}
            </button>
          {/if}
          {#if pushSubscription.errorMessage}
            <p class="error-text" role="alert">{pushSubscription.errorMessage}</p>
          {/if}
        </div>

        <div class="row vertical">
          <span class="label">{t('settings.notifications.forgetAllLabel')}</span>
          <p class="hint">
            {t('settings.notifications.forgetAllHint')}
          </p>
          <button
            type="button"
            onclick={() => void pushSubscription.destroyAll()}
            disabled={pushSubscription.busy}
          >
            {t('settings.notifications.forgetAllButton')}
          </button>
        </div>
      {:else}
        <p class="muted">{t('settings.notifications.unavailable')}</p>
      {/if}

    {:else if activeSection === 'api-keys'}
      <h2>{t('settings.apiKeys')}</h2>
      <ApiKeysForm />

    {:else if activeSection === 'privacy'}
      <h2>{t('settings.privacy.heading')}</h2>

      <div class="row vertical">
        <span class="label">{t('settings.privacy.externalImagesLabel')}</span>
        <div class="segmented" role="radiogroup" aria-label={t('settings.privacy.externalImagesAria')}>
          {#each ['never', 'per-sender', 'always'] as const as choice}
            <button
              type="button"
              role="radio"
              aria-checked={settings.imageLoadDefault === choice}
              class:on={settings.imageLoadDefault === choice}
              onclick={() => settings.setImageLoadDefault(choice)}
            >
              {choice === 'never'
                ? t('settings.privacy.externalImages.never')
                : choice === 'per-sender'
                  ? t('settings.privacy.externalImages.perSender')
                  : t('settings.privacy.externalImages.always')}
            </button>
          {/each}
        </div>
        <p class="hint">
          {@html t('settings.privacy.externalImagesHint', {
            neverEm: `<em>${t('settings.privacy.externalImages.never')}</em>`,
            perSenderEm: `<em>${t('settings.privacy.externalImages.perSender')}</em>`,
          })}
        </p>
      </div>

      {#if settings.imageLoadDefault === 'per-sender'}
        <h3>{t('settings.privacy.allowedSendersHeading')}</h3>
        {#if settings.imageAllowList.length === 0}
          <p class="muted">{t('settings.privacy.allowedSendersEmpty')}</p>
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
                  {t('common.remove')}
                </button>
              </li>
            {/each}
          </ul>
        {/if}
      {/if}

      <h3>{t('settings.privacy.autocompleteHeading')}</h3>
      <PrivacyForm />

    {:else if activeSection === 'diagnostics'}
      <h2>{t('settings.diagnostics.heading')}</h2>
      <DiagnosticsForm />

    {:else if activeSection === 'about'}
      <h2>{t('settings.about')}</h2>
      <div class="row">
        <span class="label">{t('settings.about.heroldVersion')}</span>
        <span class="value mono">{APP_VERSION}</span>
      </div>
      <div class="row">
        <span class="label">{t('settings.about.jmapApiUrl')}</span>
        <span class="value mono">{auth.session?.apiUrl ?? '—'}</span>
      </div>
      <div class="row">
        <span class="label">{t('settings.about.eventSourceUrl')}</span>
        <span class="value mono">{auth.session?.eventSourceUrl ?? '—'}</span>
      </div>
      <div class="row">
        <span class="label">{t('settings.about.sessionState')}</span>
        <span class="value mono">{auth.session?.state ?? '—'}</span>
      </div>

      <h3>{t('settings.about.capabilitiesHeading')}</h3>
      {#if capabilityList.length === 0}
        <p class="muted">{t('settings.about.noSession')}</p>
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
  .ext-sub-hint {
    margin-top: var(--spacing-04);
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

  .prompt-display {
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    color: var(--text-primary);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-03) var(--spacing-04);
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 200px;
    overflow-y: auto;
    margin: 0;
  }

  .disclosure-note {
    background: var(--layer-01);
    border-left: 3px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-03) var(--spacing-04);
    margin-top: var(--spacing-02);
  }

  .disclosure-note p {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }

  .error-text {
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  .primary-action {
    padding: var(--spacing-03) var(--spacing-05);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
    width: fit-content;
  }
  .primary-action:hover:not(:disabled) {
    filter: brightness(1.1);
  }
  .primary-action:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  @media (max-width: 768px) {
    .settings-shell {
      grid-template-columns: 1fr;
      padding: var(--spacing-04);
    }
  }

  /* External submission identity badges (REQ-MAIL-SUBMIT-04) */
  .identity-row-wrapper {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .identity-badge-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    flex-wrap: wrap;
  }

  .identity-label {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    font-weight: 500;
    word-break: break-all;
    flex: 1;
  }

  .badge {
    display: inline-flex;
    align-items: center;
    padding: 2px var(--spacing-02);
    border-radius: var(--radius-pill);
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.02em;
    cursor: pointer;
    border: none;
    transition: filter var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
    flex-shrink: 0;
  }

  .badge-external {
    background: color-mix(in srgb, var(--interactive) 15%, transparent);
    color: var(--interactive);
    border: 1px solid color-mix(in srgb, var(--interactive) 40%, transparent);
  }

  .badge-external:hover {
    filter: brightness(1.1);
  }

  .badge-alert {
    background: color-mix(in srgb, var(--support-warning) 15%, transparent);
    color: color-mix(in srgb, var(--support-warning) 90%, var(--text-primary));
    border: 1px solid color-mix(in srgb, var(--support-warning) 50%, transparent);
    animation: pulse-alert 2s ease-in-out infinite;
  }

  .badge-alert:hover {
    filter: brightness(1.05);
  }

  .badge-link {
    font-size: var(--type-body-compact-01-size);
    color: var(--interactive);
    background: none;
    border: none;
    cursor: pointer;
    padding: 2px var(--spacing-02);
    border-radius: var(--radius-sm);
    flex-shrink: 0;
  }

  .badge-link:hover {
    text-decoration: underline;
  }

  @keyframes pulse-alert {
    0%, 100% { opacity: 1; }
    50% { opacity: 0.7; }
  }

  @media (prefers-reduced-motion: reduce) {
    .badge-alert {
      animation: none;
    }
  }

  .signout-btn {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--layer-02);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    min-height: var(--touch-min);
    font-size: var(--type-body-compact-01-size);
    flex-shrink: 0;
  }
  .signout-btn:hover {
    background: var(--support-error);
    color: var(--text-on-color);
    border-color: var(--support-error);
  }
</style>
