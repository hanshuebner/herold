<script lang="ts">
  /**
   * Unified active-credentials panel (issue #224, supersedes the
   * sessions-only view that previously lived here -- REQ-AS-30..34).
   *
   * Lists every way something is currently authenticated as the caller --
   * browser sessions, device tokens, and OAuth2 native-client grants --
   * via GET /api/v1/auth/credentials, grouped by kind. Each row shows the
   * attributes needed to recognise the credential: a device/browser or
   * client label, created time, last-used time, expiry (where the kind
   * carries one), and IP / user agent (sessions only, the only kind the
   * server records those for). The current session is marked with a
   * "This session" chip.
   *
   * Revocation is per-row via DELETE /api/v1/auth/credentials/{kind}/{id}.
   * The store always re-fetches the list after a revoke rather than
   * removing the row client-side (credentials.svelte.ts) -- so a
   * device-token revoke, an oauth2_grant revoke (which cascades to the
   * whole refresh-token family server-side), and a same-session revoke
   * all end up reflecting the server's post-revoke truth, not a guess
   * about what the cascade did. Revoking the current session clears
   * cookies server-side; the follow-up re-fetch legitimately 401s and
   * hands off to the global forced-login flow (REQ-AS-10).
   */
  import { auth } from '../../lib/auth/auth.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
  import { confirm } from '../../lib/dialog/confirm.svelte';
  import { credentials, type CredentialDTO, type CredentialKind } from '../../lib/settings/credentials.svelte';
  import { localeTag, t } from '../../lib/i18n/i18n.svelte';
  import Button from '@herold/design-system/Button.svelte';

  $effect(() => {
    if (auth.principalId) {
      void credentials.load();
    }
  });

  function formatDate(iso: string | undefined): string {
    if (!iso) return '—';
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toLocaleString(localeTag(), {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    });
  }

  /** Short device/browser label derived client-side from the session's user-agent
   *  string -- the server never parses UA strings (REQ-AS-31). */
  function deviceLabel(ua: string | undefined): string {
    if (!ua) return t('settings.credentials.unknownDevice');
    if (/iPhone/.test(ua)) return 'iPhone';
    if (/iPad/.test(ua)) return 'iPad';
    if (/Android/.test(ua)) return 'Android';
    if (/Macintosh/.test(ua)) return 'Mac';
    if (/Windows/.test(ua)) return 'Windows PC';
    if (/Linux/.test(ua)) return 'Linux';
    return ua.length > 40 ? ua.slice(0, 40) + '...' : ua;
  }

  /** The row's user-facing title: device label for sessions, the server-supplied
   *  label (device name / OAuth2 client name) for the other two kinds. */
  function rowTitle(c: CredentialDTO): string {
    if (c.kind === 'session') return deviceLabel(c.user_agent);
    if (c.label) return c.label;
    return c.kind === 'oauth2_grant' ? c.client_id ?? t('settings.credentials.kind.oauth2Grant') : t('settings.credentials.kind.deviceToken');
  }

  function kindLabel(kind: CredentialKind): string {
    switch (kind) {
      case 'session':
        return t('settings.credentials.kind.session');
      case 'device_token':
        return t('settings.credentials.kind.deviceToken');
      case 'oauth2_grant':
        return t('settings.credentials.kind.oauth2Grant');
    }
  }

  let sessions = $derived(credentials.items.filter((c) => c.kind === 'session'));
  let deviceTokens = $derived(credentials.items.filter((c) => c.kind === 'device_token'));
  let oauthGrants = $derived(credentials.items.filter((c) => c.kind === 'oauth2_grant'));
  let hasOtherSessions = $derived(sessions.some((s) => !s.is_current));

  async function revokeOne(c: CredentialDTO): Promise<void> {
    const isCurrent = c.is_current;
    const ok = await confirm.ask({
      title: isCurrent ? t('settings.credentials.signOut') : t('settings.credentials.revoke'),
      message: isCurrent
        ? t('settings.credentials.signOutConfirm')
        : t('settings.credentials.revokeConfirm', { name: rowTitle(c) }),
      confirmLabel: isCurrent ? t('settings.credentials.signOut') : t('settings.credentials.revoke'),
      cancelLabel: t('common.cancel'),
      kind: 'danger',
    });
    if (!ok) return;
    try {
      await credentials.revoke(c.kind, c.id);
      if (isCurrent) {
        // The revoke cleared this browser's cookies server-side; the
        // follow-up re-fetch inside credentials.revoke() 401s, which the
        // shared REST client's global _onUnauthenticated callback already
        // turned into a forced-login transition (see credentials.svelte.ts).
        // Nothing further to do here.
        return;
      }
      toast.show({ message: t('settings.credentials.revoked'), timeoutMs: 4000 });
    } catch {
      toast.show({
        message: t('settings.credentials.revokeError'),
        kind: 'error',
        timeoutMs: 0,
      });
    }
  }

  async function revokeAllOtherSessions(): Promise<void> {
    const others = sessions.filter((s) => !s.is_current);
    if (others.length === 0) return;
    const ok = await confirm.ask({
      title: t('settings.credentials.revokeAllSessions'),
      message: t('settings.credentials.revokeAllSessionsConfirm'),
      confirmLabel: t('settings.credentials.revokeAllSessions'),
      cancelLabel: t('common.cancel'),
      kind: 'danger',
    });
    if (!ok) return;
    let anyFailed = false;
    for (const s of others) {
      try {
        await credentials.revoke(s.kind, s.id);
      } catch {
        anyFailed = true;
      }
    }
    if (anyFailed) {
      toast.show({
        message: t('settings.credentials.revokeError'),
        kind: 'error',
        timeoutMs: 0,
      });
    } else {
      toast.show({ message: t('settings.credentials.allSessionsRevoked'), timeoutMs: 4000 });
    }
  }
</script>

{#if credentials.loading}
  <p aria-label={t('settings.credentials.loadingAria')} class="muted">{t('common.loading')}</p>
{:else if credentials.errorMessage}
  <p class="error">{credentials.errorMessage}</p>
{:else if credentials.items.length === 0}
  <p class="muted">{t('settings.credentials.empty')}</p>
{:else}
  {#if sessions.length > 0}
    <h3>{t('settings.credentials.group.sessions')}</h3>
    <ul class="credential-list" data-testid="credentials-sessions">
      {#each sessions as c (c.id)}
        <li class="credential-row" class:current={c.is_current}>
          <div class="credential-info">
            <div class="credential-title">
              {rowTitle(c)}
              {#if c.is_current}
                <span class="chip current-chip">{t('settings.credentials.thisSession')}</span>
              {/if}
            </div>
            <div class="credential-meta">
              <span>{t('settings.credentials.ip')}: {c.last_seen_ip || '—'}</span>
              <span>{t('settings.credentials.createdAt')}: {formatDate(c.created_at)}</span>
              <span>{t('settings.credentials.lastUsed')}: {formatDate(c.last_used_at)}</span>
            </div>
          </div>
          <div class="credential-actions">
            <Button variant="danger" compact onclick={() => void revokeOne(c)}>
              {c.is_current ? t('settings.credentials.signOut') : t('settings.credentials.revoke')}
            </Button>
          </div>
        </li>
      {/each}
    </ul>

    <div class="bulk-actions">
      <Button variant="danger" compact disabled={!hasOtherSessions} onclick={() => void revokeAllOtherSessions()}>
        {t('settings.credentials.revokeAllSessions')}
      </Button>
    </div>
  {/if}

  {#if deviceTokens.length > 0}
    <h3>{t('settings.credentials.group.deviceTokens')}</h3>
    <ul class="credential-list" data-testid="credentials-device-tokens">
      {#each deviceTokens as c (c.id)}
        <li class="credential-row">
          <div class="credential-info">
            <div class="credential-title">{rowTitle(c)}</div>
            <div class="credential-meta">
              <span>{t('settings.credentials.createdAt')}: {formatDate(c.created_at)}</span>
              <span>{t('settings.credentials.lastUsed')}: {formatDate(c.last_used_at)}</span>
            </div>
          </div>
          <div class="credential-actions">
            <Button variant="danger" compact onclick={() => void revokeOne(c)}>
              {t('settings.credentials.revoke')}
            </Button>
          </div>
        </li>
      {/each}
    </ul>
  {/if}

  {#if oauthGrants.length > 0}
    <h3>{t('settings.credentials.group.oauthGrants')}</h3>
    <ul class="credential-list" data-testid="credentials-oauth-grants">
      {#each oauthGrants as c (c.id)}
        <li class="credential-row">
          <div class="credential-info">
            <div class="credential-title">{rowTitle(c)}</div>
            <div class="credential-meta">
              <span>{t('settings.credentials.createdAt')}: {formatDate(c.created_at)}</span>
              <span>{t('settings.credentials.lastUsed')}: {formatDate(c.last_used_at)}</span>
              <span>{t('settings.credentials.expiresAt')}: {formatDate(c.expires_at)}</span>
            </div>
          </div>
          <div class="credential-actions">
            <Button variant="danger" compact onclick={() => void revokeOne(c)}>
              {t('settings.credentials.revoke')}
            </Button>
          </div>
        </li>
      {/each}
    </ul>
  {/if}
{/if}

<style>
  h3 {
    font-size: var(--type-heading-compact-02-size);
    line-height: var(--type-heading-compact-02-line);
    font-weight: var(--type-heading-compact-02-weight);
    margin: var(--spacing-05) 0 var(--spacing-02);
    color: var(--text-secondary);
  }
  h3:first-child {
    margin-top: 0;
  }

  .credential-list {
    list-style: none;
    padding: 0;
    margin: 0 0 var(--spacing-04) 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .credential-row {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--spacing-04);
    padding: var(--spacing-04);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    background: var(--layer-01);
  }

  .credential-row.current {
    border-color: var(--interactive);
  }

  .credential-info {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
    min-width: 0;
  }

  .credential-title {
    font-weight: 600;
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    flex-wrap: wrap;
    word-break: break-word;
  }

  .chip {
    display: inline-block;
    padding: 1px var(--spacing-03);
    border-radius: var(--radius-pill);
    font-size: var(--type-body-compact-01-size);
    font-weight: 500;
  }

  .current-chip {
    background: var(--interactive);
    color: var(--text-on-color);
  }

  .credential-meta {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-03);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }

  .credential-actions {
    flex-shrink: 0;
  }

  .bulk-actions {
    margin-top: var(--spacing-02);
    margin-bottom: var(--spacing-05);
  }

  .muted {
    color: var(--text-secondary);
  }

  .error {
    color: var(--support-error);
  }
</style>
