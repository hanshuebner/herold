<script lang="ts">
  /**
   * Thread-level Unsubscribe affordance (REQ-UNS-01..43).
   *
   * Shown once per thread, not per message (REQ-UNS-11), scanning every
   * message in the thread for a `List-Unsubscribe` header and using the
   * most recent one that carries a usable mechanism. Absent entirely
   * when no message in the thread advertises one (REQ-UNS-03 -- no
   * body-text-scraping fallback).
   */
  import { compose } from '../compose/compose.svelte';
  import { toast } from '../toast/toast.svelte';
  import { t } from '../i18n/i18n.svelte';
  import { jmap } from '../jmap/client';
  import { Capability } from '../jmap/types';
  import type { Email } from './types';
  import { mail } from './store.svelte';
  import {
    chooseUnsubscribeMechanism,
    parseAngleBracketUrls,
    parseMailtoUri,
    type UnsubscribeMechanism,
  } from './list-headers';
  import { postOneClickUnsubscribe } from './unsubscribe';
  import { recordUnsubscribed } from './unsubscribed-from';

  interface Props {
    emails: Email[];
  }
  let { emails }: Props = $props();

  interface Source {
    email: Email;
    mechanism: UnsubscribeMechanism;
  }

  let source = $derived.by<Source | null>(() => {
    for (let i = emails.length - 1; i >= 0; i--) {
      const email = emails[i];
      if (!email) continue;
      const mechanism = chooseUnsubscribeMechanism(
        email['header:List-Unsubscribe:asText'],
        email['header:List-Unsubscribe-Post:asText'],
      );
      if (mechanism) return { email, mechanism };
    }
    return null;
  });

  let inFlight = $state(false);

  function senderDisplay(email: Email): string {
    const from = email.from?.[0];
    return from?.name?.trim() || from?.email || '';
  }

  /**
   * The mailto: fallback alongside a one-click mechanism, when the
   * same `List-Unsubscribe` header also advertised one (REQ-UNS-22).
   * Independent of the chosen mechanism -- RFC 8058 senders commonly
   * list an HTTPS one-click URL and a mailto alternative together.
   */
  function mailtoFallback(email: Email): string | null {
    const urls = parseAngleBracketUrls(email['header:List-Unsubscribe:asText']);
    return urls.find((u) => u.scheme === 'mailto')?.url ?? null;
  }

  function openMailtoCompose(uri: string): void {
    const fields = parseMailtoUri(uri);
    compose.openWith({
      to: fields.to,
      subject: fields.subject,
      body: fields.body,
      skipSignature: true,
    });
  }

  /**
   * Show the failure toast for a one-click POST that did not succeed
   * (`status: "failed"` or `"unsupported"`, or a thrown transport/method
   * error). Offers the HTTPS link in a new tab (REQ-UNS-21) and, when
   * present, the mailto fallback (REQ-UNS-22) directly from the toast.
   */
  function showFailureToast(url: string, email: Email): void {
    const mailto = mailtoFallback(email);
    toast.show({
      message: t('unsubscribe.toast.failed'),
      kind: 'error',
      timeoutMs: 8000,
      detail: url,
      actionLabel: t('unsubscribe.toast.openLink'),
      undo: () => {
        window.open(url, '_blank', 'noopener,noreferrer');
      },
      ...(mailto
        ? {
            secondaryActionLabel: t('unsubscribe.toast.sendEmail'),
            secondaryAction: () => {
              openMailtoCompose(mailto);
            },
          }
        : {}),
    });
  }

  async function runOneClick(url: string, email: Email): Promise<void> {
    const accountId = mail.emailAccountId.get(email.id) ?? mail.mailAccountId;
    if (!accountId) {
      showFailureToast(url, email);
      return;
    }
    inFlight = true;
    try {
      const result = await postOneClickUnsubscribe(accountId, email.id);
      if (result.status === 'ok') {
        const sender = email.from?.[0]?.email;
        if (sender) recordUnsubscribed(sender);
        toast.show({
          message: t('unsubscribe.toast.success', { sender: senderDisplay(email) }),
        });
      } else {
        showFailureToast(url, email);
      }
    } catch {
      showFailureToast(url, email);
    } finally {
      inFlight = false;
    }
  }

  function handleClick(): void {
    const src = source;
    if (!src || inFlight) return;
    const { mechanism, email } = src;
    if (mechanism.kind === 'one-click') {
      if (jmap.hasCapability(Capability.HeroldEmailUnsubscribe)) {
        // REQ-UNS-30: no confirmation dialog -- the whole point of RFC 8058.
        void runOneClick(mechanism.url, email);
      } else {
        // The server does not support Email/unsubscribe: fall back to
        // the plain-https behaviour (REQ-UNS-21) rather than a browser
        // fetch that CORS would silently discard the response of.
        window.open(mechanism.url, '_blank', 'noopener,noreferrer');
      }
      return;
    }
    if (mechanism.kind === 'https') {
      // REQ-UNS-31: no confirmation before opening the tab.
      window.open(mechanism.url, '_blank', 'noopener,noreferrer');
      return;
    }
    if (mechanism.kind === 'mailto') {
      // REQ-UNS-32: confirmation-by-send -- the user must still hit Send.
      openMailtoCompose(mechanism.url);
      return;
    }
    // REQ-UNS-04: cleartext-only -- never auto-click, just warn.
    toast.show({ message: t('unsubscribe.cleartextWarning'), kind: 'info' });
  }
</script>

{#if source}
  <button
    type="button"
    class="unsubscribe-btn"
    disabled={inFlight}
    onclick={handleClick}
  >
    {t('unsubscribe.button')}
  </button>
{/if}

<style>
  .unsubscribe-btn {
    display: inline-flex;
    align-items: center;
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-pill);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
    white-space: nowrap;
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
  }
  .unsubscribe-btn:hover:not(:disabled) {
    background: var(--layer-03);
    color: var(--text-primary);
  }
  .unsubscribe-btn:disabled {
    opacity: 0.6;
    cursor: default;
  }

  @media print {
    .unsubscribe-btn {
      display: none;
    }
  }
</style>
