<script lang="ts">
  /**
   * Singleton connection-test result modal (re #131 item 2).
   *
   * Mounted once in Shell.svelte, same pattern as ConfirmDialog / PromptDialog.
   * Rendered reactively from the connectionTest store
   * (lib/dialog/connection-test.svelte.ts).
   *
   * Behavior:
   *   - Shows the result of POST /api/v1/identities/{id}/submission/test.
   *   - On success: green heading, server/email shown, probe description.
   *   - On failure: red heading, verbatim SMTP diagnostic.
   *   - OAuth failure: adds "Neu autorisieren" button that opens a popup
   *     (display=popup OAuth start), listens for postMessage, and updates
   *     the display with the re-auth result.
   *   - postMessage handler validates event.origin === window.location.origin
   *     (never "*") and checks event.data.type === "herold:oauth-result".
   */
  import { connectionTest } from './connection-test.svelte';
  import { startOAuthPopup } from '../api/identity-submission';
  import { ApiError } from '../api/client';
  import Button from '@herold/design-system/Button.svelte';
  import { t } from '../i18n/i18n.svelte';

  // ── Popup re-auth state ───────────────────────────────────────────────────

  /**
   * Current display state. The initial value comes from connectionTest.pending;
   * it may be updated in-place when the re-auth popup completes.
   *  - 'result': showing the initial test result
   *  - 'authorizing': popup is open, waiting for postMessage
   *  - 'reauth-ok': popup completed successfully
   *  - 'reauth-fail': popup reported failure
   */
  type Phase = 'result' | 'authorizing' | 'reauth-ok' | 'reauth-fail';

  let phase = $state<Phase>('result');
  let reauthDetail = $state('');
  let popupError = $state('');

  // Reset internal state whenever a new dialog opens.
  $effect(() => {
    if (connectionTest.pending) {
      phase = 'result';
      reauthDetail = '';
      popupError = '';
    }
  });

  // ── Keyboard handler ─────────────────────────────────────────────────────

  // Escape closes the modal (capture phase so it beats compose/other bindings).
  $effect(() => {
    if (!connectionTest.pending) return;
    const onKey = (e: KeyboardEvent): void => {
      if (e.key === 'Escape' && phase !== 'authorizing') {
        e.preventDefault();
        e.stopPropagation();
        close();
      }
    };
    document.addEventListener('keydown', onKey, { capture: true });
    return () => document.removeEventListener('keydown', onKey, { capture: true });
  });

  // ── Helpers ───────────────────────────────────────────────────────────────

  function close(): void {
    phase = 'result';
    connectionTest.close();
  }

  /**
   * Open the re-authorization popup and listen for the completion message.
   *
   * The popup is opened without noopener/noreferrer so window.opener is
   * accessible inside the completion page served by the OAuth callback.
   * postMessage targetOrigin is window.location.origin (never "*").
   */
  async function reauthorize(): Promise<void> {
    const ctx = connectionTest.pending;
    if (!ctx || !ctx.provider) return;

    popupError = '';
    phase = 'authorizing';

    let popup: Window | null = null;
    try {
      popup = await startOAuthPopup(ctx.identityId, ctx.provider as 'gmail' | 'm365');
    } catch (err) {
      const msg =
        err instanceof ApiError
          ? err.message
          : err instanceof Error
            ? err.message
            : String(err);
      popupError = msg;
      phase = 'result';
      return;
    }

    if (!popup) {
      // window.open returned null (blocked by the browser).
      popupError = t('settings.submission.testModal.popupBlocked');
      phase = 'result';
      return;
    }

    // Listen for the completion message from the popup completion page.
    let settled = false;

    const onMessage = (event: MessageEvent): void => {
      // Validate origin (same-origin, never "*").
      if (event.origin !== window.location.origin) return;
      if (
        typeof event.data !== 'object' ||
        event.data === null ||
        event.data.type !== 'herold:oauth-result'
      ) {
        return;
      }
      if (settled) return;
      settled = true;
      window.removeEventListener('message', onMessage);
      clearInterval(closedPoll);

      const { ok, detail } = event.data as { ok: boolean; detail: string };
      if (ok) {
        phase = 'reauth-ok';
      } else {
        reauthDetail = detail ?? '';
        phase = 'reauth-fail';
      }
    };
    window.addEventListener('message', onMessage);

    // Poll for the popup being closed without sending a message (user closed
    // the window manually before completing the flow).
    const closedPoll = setInterval(() => {
      if (popup!.closed && !settled) {
        settled = true;
        window.removeEventListener('message', onMessage);
        clearInterval(closedPoll);
        // Revert to the original result display; the user cancelled.
        phase = 'result';
      }
    }, 500);
  }

  // ── Derived values ────────────────────────────────────────────────────────

  let ctx = $derived(connectionTest.pending);
  let showReauthBtn = $derived(
    ctx !== null && ctx.isOAuth && ctx.provider !== null && phase === 'result' && !ctx.ok,
  );
</script>

{#if ctx}
  <div
    class="backdrop"
    aria-hidden="true"
    onclick={() => { if (phase !== 'authorizing') close(); }}
  ></div>
  <div class="modal-wrapper">
    <div
      class="modal"
      role="alertdialog"
      aria-modal="true"
      aria-labelledby="ctd-title"
      aria-describedby="ctd-body"
    >
      {#if phase === 'authorizing'}
        <!-- Re-auth in progress -->
        <h2 id="ctd-title" class="title">{t('settings.submission.testModal.authorizing')}</h2>
        <div id="ctd-body" class="body-group">
          <div class="spinner" role="status" aria-label={t('settings.submission.testModal.authorizing')}></div>
          <p class="body-hint">{t('settings.submission.testModal.authorizingHint')}</p>
        </div>
        <div class="actions">
          <Button variant="secondary" onclick={close}>
            {t('settings.submission.testModal.close')}
          </Button>
        </div>

      {:else if phase === 'reauth-ok'}
        <!-- Re-auth succeeded -->
        <h2 id="ctd-title" class="title title-ok">
          {t('settings.submission.testModal.reauthorizeSuccessTitle')}
        </h2>
        <div id="ctd-body" class="body-group">
          <p class="body">{t('settings.submission.testModal.reauthorizeSuccessBody')}</p>
        </div>
        <div class="actions">
          <Button variant="primary" onclick={close}>
            {t('settings.submission.testModal.ok')}
          </Button>
        </div>

      {:else if phase === 'reauth-fail'}
        <!-- Re-auth failed -->
        <h2 id="ctd-title" class="title title-fail">
          {t('settings.submission.testModal.reauthorizeFailTitle')}
        </h2>
        <div id="ctd-body" class="body-group">
          <p class="body">{t('settings.submission.testModal.reauthorizeFailBody')}</p>
          <pre class="diagnostic">{reauthDetail}</pre>
          {#if ctx.provider}
            <p class="hint">{t('settings.submission.testModal.reauthorizeHint')}</p>
          {/if}
        </div>
        <div class="actions">
          {#if ctx.provider}
            <Button variant="secondary" onclick={() => void reauthorize()}>
              {t('settings.submission.testModal.reauthorize')}
            </Button>
          {/if}
          <Button variant="primary" onclick={close}>
            {t('settings.submission.testModal.close')}
          </Button>
        </div>

      {:else if ctx.ok}
        <!-- Success -->
        <h2 id="ctd-title" class="title title-ok">
          {t('settings.submission.testModal.successTitle')}
        </h2>
        <div id="ctd-body" class="body-group">
          <p class="body">
            {ctx.serverHost
              ? t('settings.submission.testModal.successBody', {
                  host: ctx.serverHost,
                  email: ctx.selfEmail ?? '',
                })
              : t('settings.submission.testModal.successBodyNoHost', {
                  email: ctx.selfEmail ?? '',
                })}
          </p>
          <p class="probe-description">{t('settings.submission.testModal.probeDescription')}</p>
        </div>
        <div class="actions">
          <Button variant="primary" onclick={close}>
            {t('settings.submission.testModal.ok')}
          </Button>
        </div>

      {:else}
        <!-- Failure -->
        <h2 id="ctd-title" class="title title-fail">
          {t('settings.submission.testModal.failTitle')}
        </h2>
        <div id="ctd-body" class="body-group">
          <p class="body">{t('settings.submission.testModal.failBody')}</p>
          <pre class="diagnostic">{ctx.detail}</pre>
          {#if showReauthBtn}
            <p class="hint">{t('settings.submission.testModal.reauthorizeHint')}</p>
          {/if}
          {#if popupError}
            <p class="popup-error" role="alert">{popupError}</p>
          {/if}
        </div>
        <div class="actions">
          {#if showReauthBtn}
            <Button variant="secondary" onclick={() => void reauthorize()}>
              {t('settings.submission.testModal.reauthorize')}
            </Button>
          {/if}
          <Button variant="primary" onclick={close}>
            {t('settings.submission.testModal.close')}
          </Button>
        </div>
      {/if}
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.6);
    z-index: 999;
    cursor: default;
  }

  .modal-wrapper {
    position: fixed;
    inset: 0;
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
    pointer-events: none;
    padding: var(--spacing-05);
  }

  .modal {
    pointer-events: auto;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    padding: var(--spacing-06);
    max-width: 480px;
    width: 100%;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.5);
  }

  .title {
    margin: 0;
    font-size: var(--type-heading-01-size);
    font-weight: var(--type-heading-01-weight);
    line-height: var(--type-heading-01-line);
    color: var(--text-primary);
  }

  .title-ok {
    color: var(--support-success);
  }

  .title-fail {
    color: var(--support-error);
  }

  .body-group {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .body {
    margin: 0;
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    color: var(--text-secondary);
  }

  .probe-description {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
    color: var(--text-helper);
  }

  .diagnostic {
    margin: 0;
    padding: var(--spacing-03) var(--spacing-04);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-left: 3px solid var(--support-error);
    border-radius: var(--radius-md);
    color: var(--support-error);
    font-family: var(--font-mono, monospace);
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
    white-space: pre-wrap;
    word-break: break-word;
  }

  .hint {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
    color: var(--text-helper);
  }

  .popup-error {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    color: var(--support-error);
    padding: var(--spacing-02) var(--spacing-03);
    background: color-mix(in srgb, var(--support-error) 10%, transparent);
    border-radius: var(--radius-md);
    border-left: 3px solid var(--support-error);
  }

  .body-hint {
    margin: 0;
    font-size: var(--type-body-compact-01-size);
    color: var(--text-helper);
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-03);
    margin-top: var(--spacing-02);
  }

  /* Loading spinner (re-auth in progress). */
  .spinner {
    width: 20px;
    height: 20px;
    border: 2px solid var(--layer-03);
    border-top-color: var(--interactive);
    border-radius: 50%;
    animation: spin 800ms linear infinite;
    align-self: flex-start;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  @media (prefers-reduced-motion: reduce) {
    .spinner { animation: none; }
  }
</style>
