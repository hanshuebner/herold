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
  import IdentityList from './settings/IdentityList.svelte';
  import IdentityEditPage from './settings/IdentityEditPage.svelte';
  import IdentityVerifyDialog from './settings/IdentityVerifyDialog.svelte';
  import AddIdentityWizard from './settings/AddIdentityWizard.svelte';
  import SecurityForm from './settings/SecurityForm.svelte';
  import ApiKeysForm from './settings/ApiKeysForm.svelte';
  import VacationForm from './settings/VacationForm.svelte';
  import SieveForm from './settings/SieveForm.svelte';
  import CategoriesForm from './settings/CategoriesForm.svelte';
  import FiltersForm from './settings/FiltersForm.svelte';
  import TaggedAddressesForm from './settings/TaggedAddressesForm.svelte';
  import PrivacyForm from './settings/PrivacyForm.svelte';
  import ImageProcessingForm from './settings/ImageProcessingForm.svelte';
  import DiagnosticsForm from './settings/DiagnosticsForm.svelte';
  import SharedFilesForm from './settings/SharedFilesForm.svelte';
  import { Capability } from '../lib/jmap/types';
  import { jmap } from '../lib/jmap/client';
  import { hasFileShares } from '../lib/jmap/file-shares';
  import { LOCALES, type Locale } from '../lib/i18n/i18n.svelte';
  import { t } from '../lib/i18n/i18n.svelte';
  import { llmTransparency } from '../lib/llm/transparency.svelte';
  import { pushSubscription } from '../lib/push/push-subscription.svelte';
  import { sounds } from '../lib/notifications/sounds.svelte';
  import { hasExternalSubmission } from '../lib/auth/capabilities';
  import { submissionStore } from '../lib/identities/identity-submission.svelte';
  import Button from '@herold/design-system/Button.svelte';
  import {
    postVerifyResend,
    retryAfterOf,
  } from '../lib/api/identity-verify';
  import { domainOf } from '../lib/identities/wizard-validators';
  import { ApiError } from '../lib/api/client';
  import { toast } from '../lib/toast/toast.svelte';
  import type { Identity } from '../lib/mail/types';

  // Hydrate the sounds toggle from localStorage on mount.
  sounds.hydrate();

  // Section order: Account, Security, Appearance, Mail, Categories, Filters,
  // Notifications, API keys, Privacy, Shared files, Diagnostics, About.
  type Section =
    | 'account'
    | 'security'
    | 'appearance'
    | 'mail'
    | 'categories'
    | 'filters'
    | 'tagged-addresses'
    | 'notifications'
    | 'api-keys'
    | 'privacy'
    | 'shared-files'
    | 'diagnostics'
    | 'about';

  let hasCategorise = $derived(jmap.hasCapability(Capability.HeroldCategorise));
  let hasManagedRules = $derived(jmap.hasCapability(Capability.HeroldManagedRules));
  let hasTaggedAddresses = $derived(jmap.hasCapability(Capability.HeroldTaggedAddresses));
  let hasLLMTransparency = $derived(jmap.hasCapability(Capability.HeroldLLMTransparency));
  let hasPush = $derived(jmap.hasCapability(Capability.HeroldPush));
  let hasSharedFiles = $derived(hasFileShares());

  let SECTIONS = $derived.by<{ id: Section; label: string }[]>(() => {
    const result: { id: Section; label: string }[] = [
      { id: 'account', label: t('settings.account') },
      { id: 'security', label: t('settings.security') },
      { id: 'appearance', label: t('settings.appearance') },
      { id: 'mail', label: t('settings.mail') },
    ];
    if (hasCategorise) result.push({ id: 'categories', label: t('settings.categories.heading') });
    if (hasManagedRules) result.push({ id: 'filters', label: t('settings.filters.heading') });
    if (hasTaggedAddresses)
      result.push({ id: 'tagged-addresses', label: t('settings.taggedAddresses.heading') });
    // Notifications section always shown (in-app sounds; push if available).
    result.push({ id: 'notifications', label: t('settings.notifications') });
    result.push({ id: 'api-keys', label: t('settings.apiKeys') });
    result.push({ id: 'privacy', label: t('settings.privacy.heading') });
    if (hasSharedFiles) result.push({ id: 'shared-files', label: 'Shared files' });
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

  // Lazy-load identities when the user opens settings — they are needed
  // for Account section and may not have been fetched yet (e.g. straight
  // navigation to /settings without loading the inbox first).
  $effect(() => {
    if (mail.identities.size === 0) void mail.loadIdentities();
  });

  // ── Identity editor sub-page (REQ-SET-IDENT-10) ─────────────────────────
  //
  // The editor is a dedicated sub-page at /#/settings/identities/:id, not
  // a modal. Navigation drives it; there is no local "open dialog" state.

  let showExtSub = $derived(hasExternalSubmission());

  /** Whether the editor sub-page should scroll to the submission section. */
  let editScrollToSubmission = $state(false);

  /** The identity id taken from the /settings/identities/:id route, if any. */
  let editIdentityId = $derived(
    router.parts[1] === 'identities' ? (router.parts[2] ?? null) : null,
  );

  /** The Identity object the editor sub-page renders, or null. */
  let editIdentity = $derived<Identity | null>(
    editIdentityId ? (mail.identities.get(editIdentityId) ?? null) : null,
  );

  function navigateToEditor(identity: Identity): void {
    router.navigate(`/settings/identities/${identity.id}`);
  }

  function closeEditor(): void {
    editScrollToSubmission = false;
    router.navigate('/settings/account');
  }

  // Pre-load the submission status when the editor route opens, so the
  // external-submission section renders without a per-section lazy load.
  $effect(() => {
    if (editIdentityId && showExtSub) {
      void submissionStore.forIdentity(editIdentityId).load();
    }
  });

  // ── Add-identity wizard (REQ-SET-IDENT-30) ───────────────────────────────

  let showAddWizard = $state(false);

  function openAddWizard(): void {
    showAddWizard = true;
  }

  function closeAddWizard(): void {
    showAddWizard = false;
  }

  /**
   * The set of email domains hosted on this herold. Derived from the
   * principal's username plus the verified identities already in the
   * cache — the synthesised default identity is verified-by-construction
   * (REQ-IDENT-02), and any other verified identity whose row carries
   * no external-submission record is hosted-by-construction.
   *
   * The wizard's Step 3 uses this set to decide whether to offer the
   * "Configure external SMTP" pane (REQ-SET-IDENT-32). For a legacy
   * server where this derivation underestimates the hosted set, the
   * fallback is "treat as external" — i.e. show Step 3 — which the
   * user can skip if it does not apply.
   */
  let hostedDomains = $derived.by<Set<string>>(() => {
    const set = new Set<string>();
    const principalEmail = auth.session?.username;
    if (principalEmail) {
      const dom = domainOf(principalEmail);
      if (dom !== null) set.add(dom);
    }
    return set;
  });

  // ── Verify dialog (REQ-SET-IDENT-20..21) ────────────────────────────────

  let verifyDialogIdentity = $state<Identity | null>(null);

  function openVerifyDialog(identity: Identity): void {
    verifyDialogIdentity = identity;
  }

  function closeVerifyDialog(): void {
    verifyDialogIdentity = null;
  }

  /**
   * Trigger the verification-email resend for `identity` (REQ-IDENT-36).
   * On a 429 the suite surfaces the Retry-After countdown via the toast
   * subsystem ("Try again in 47 s"); other failures surface as a plain
   * error toast. Used by the inline Resend button on each
   * verification-pending row in IdentityList.
   */
  async function resendVerificationFromList(identity: Identity): Promise<void> {
    try {
      await postVerifyResend(identity.id);
      toast.show({
        message: t('settings.identityVerify.resendOk'),
        timeoutMs: 4000,
      });
    } catch (err) {
      if (err instanceof ApiError && err.status === 429) {
        const seconds = retryAfterOf(err);
        if (seconds !== null && seconds > 0) {
          toast.show({
            message: t('settings.identityVerify.resendRateLimited', { seconds }),
            kind: 'error',
            timeoutMs: 6000,
          });
        } else {
          toast.show({
            message: t('settings.identityVerify.resendRateLimitedShort'),
            kind: 'error',
            timeoutMs: 6000,
          });
        }
        return;
      }
      const msg = err instanceof Error ? err.message : String(err);
      toast.show({ message: msg, kind: 'error', timeoutMs: 6000 });
    }
  }

  // Handle the ?identity=<id>&action=reauth route param set by the
  // compose failure toast's "Re-authenticate" button. The deep link now
  // navigates to the identity editor sub-page (REQ-MAIL-SUBMIT-04) and
  // requests a scroll to the submission section.
  $effect(() => {
    const identityParam = router.getParam('identity');
    const actionParam = router.getParam('action');
    if (
      identityParam &&
      actionParam === 'reauth' &&
      activeSection === 'account' &&
      showExtSub &&
      editIdentityId !== identityParam
    ) {
      const identity = mail.identities.get(identityParam);
      if (identity) {
        // Clear the params first so a back-navigation does not re-trigger.
        router.setParam('identity', null);
        router.setParam('action', null);
        editScrollToSubmission = true;
        router.navigate(`/settings/identities/${identityParam}`);
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
    {#if activeSection === 'account' && editIdentityId}
      <!-- REQ-SET-IDENT-10: the per-identity editor is a dedicated
           sub-page at /#/settings/identities/:id, rendered inside the
           settings content column. -->
      {#if editIdentity}
        <IdentityEditPage
          identity={editIdentity}
          onback={closeEditor}
          scrollToSubmission={editScrollToSubmission}
        />
      {:else}
        <header class="editor-missing-header">
          <Button variant="ghost" compact onclick={closeEditor}>
            {t('settings.identityEdit.back')}
          </Button>
        </header>
        <p class="muted">{t('settings.identityEdit.notFound')}</p>
      {/if}

    {:else if activeSection === 'account'}
      <h2>{t('settings.account')}</h2>

      <div class="row">
        <span class="label">{t('settings.account.signedInAs')}</span>
        <span class="value">{auth.session?.username ?? '—'}</span>
        <Button
          variant="danger"
          compact
          onclick={() => void auth.logout()}
        >
          {t('settings.account.signOut')}
        </Button>
      </div>

      <!-- REQ-SET-IDENT-01..08: identity list with verification status
           labels, a default radio, and an explicit per-row edit button.
           The editor opens as a dedicated sub-page (REQ-SET-IDENT-10).
           The add wizard / verify dialog are mounted below by this
           view; the list forwards user intent via the onadd / onverify
           / onresend callbacks. -->
      <IdentityList
        onedit={navigateToEditor}
        onadd={openAddWizard}
        onverify={openVerifyDialog}
        onresend={resendVerificationFromList}
      />

      {#if !showExtSub}
        <p class="hint ext-sub-hint">
          {@html t('settings.account.extSubHint', {
            systemToml: '<code>system.toml</code>',
            docPath: '<code>docs/manual/admin/external-smtp-submission.mdoc</code>',
          })}
        </p>
      {/if}

      {#if showAddWizard}
        <AddIdentityWizard
          {hostedDomains}
          onclose={closeAddWizard}
        />
      {/if}

      {#if verifyDialogIdentity}
        <IdentityVerifyDialog
          identity={verifyDialogIdentity}
          onclose={closeVerifyDialog}
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

    {:else if activeSection === 'tagged-addresses'}
      <h2>{t('settings.taggedAddresses.heading')}</h2>
      <TaggedAddressesForm />

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

      <div class="row vertical">
        <span class="label">{t('settings.notifications.desktopLabel')}</span>
        <p class="hint">
          {t('settings.notifications.desktopHint')}
        </p>
        <label class="switch" aria-label={t('settings.notifications.desktopAria')}>
          <input
            type="checkbox"
            checked={settings.desktopNotifEnabled}
            onchange={(e) => settings.setDesktopNotifEnabled((e.currentTarget as HTMLInputElement).checked)}
          />
          <span class="track" aria-hidden="true"></span>
        </label>
      </div>

      {#if hasPush}
        <div class="row vertical">
          <span class="label">{t('settings.notifications.pushLabel')}</span>
          <p class="hint">{t('settings.notifications.pushSubscriptionHint')}</p>
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

      <ImageProcessingForm />

      <h3>{t('settings.privacy.autocompleteHeading')}</h3>
      <PrivacyForm />

    {:else if activeSection === 'shared-files'}
      <h2>Shared files</h2>
      <p class="hint">
        Files you have shared as links. Active shares can be revoked at any time.
        Download counts reflect the last time this view was loaded (REQ-ATT-73).
      </p>
      <SharedFilesForm />

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

  .editor-missing-header {
    display: flex;
    margin-bottom: var(--spacing-04);
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

  /* Identity-row external-submission badges moved to IdentityList.svelte
     (REQ-SET-IDENT-01..08); the per-row markup that owned these classes
     no longer lives in SettingsView. The sign-out button now uses the
     shared design-system Button (danger variant). */
</style>
