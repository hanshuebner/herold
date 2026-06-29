<script lang="ts">
  /**
   * Active sessions panel (REQ-AS-30..34, re #80).
   *
   * Lists all active sessions for the current principal via
   * GET /api/v1/auth/sessions. Each row shows the user-agent string,
   * last-seen IP, sign-in time, and last-seen time. The current session
   * is identified by is_current and shows a "This session" chip.
   *
   * Actions:
   *   - Revoke (non-current session): DELETE /api/v1/auth/sessions/{id}
   *   - Sign out (current session):   DELETE /api/v1/auth/sessions/{id}
   *     Clears cookies; the server returns 204 and the next request
   *     gets 401, triggering the global re-login flow (REQ-AS-13).
   *   - Revoke all other sessions: sequential DELETE for each non-current
   *     session (no dedicated bulk endpoint).
   */
  import { auth } from '../../lib/auth/auth.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
  import { confirm } from '../../lib/dialog/confirm.svelte';
  import { get, del, UnauthenticatedError } from '../../lib/api/client';
  import { localeTag, t } from '../../lib/i18n/i18n.svelte';
  import Button from '@herold/design-system/Button.svelte';

  interface SessionDTO {
    session_id: string;
    created_at: string;
    last_seen_at: string;
    last_seen_ip: string;
    user_agent: string;
    is_current: boolean;
  }

  interface PageDTO {
    items: SessionDTO[];
    next: string | null;
  }

  let sessions = $state<SessionDTO[]>([]);
  let loading = $state(true);
  let loadError = $state<string | null>(null);

  async function loadSessions(): Promise<void> {
    loading = true;
    loadError = null;
    try {
      const result = await get<PageDTO>('/api/v1/auth/sessions');
      sessions = result.items ?? [];
    } catch (err) {
      if (err instanceof UnauthenticatedError) {
        auth.signalUnauthenticated();
        return;
      }
      loadError = t('settings.sessions.error');
    } finally {
      loading = false;
    }
  }

  $effect(() => {
    if (auth.principalId) {
      void loadSessions();
    }
  });

  async function revokeSession(session: SessionDTO): Promise<void> {
    const isCurrent = session.is_current;
    const ok = await confirm.ask({
      title: isCurrent ? t('settings.sessions.signOut') : t('settings.sessions.revoke'),
      message: isCurrent
        ? t('settings.sessions.signOutConfirm')
        : t('settings.sessions.revokeConfirm'),
      confirmLabel: isCurrent ? t('settings.sessions.signOut') : t('settings.sessions.revoke'),
      cancelLabel: t('common.cancel'),
      kind: 'danger',
    });
    if (!ok) return;
    try {
      await del<void>(`/api/v1/auth/sessions/${session.session_id}`);
      if (isCurrent) {
        // Server cleared cookies; signal unauthenticated so the app re-prompts.
        auth.signalUnauthenticated();
        return;
      }
      sessions = sessions.filter((s) => s.session_id !== session.session_id);
      toast.show({ message: t('settings.sessions.revoked'), timeoutMs: 4000 });
    } catch (err) {
      toast.show({
        message: t('settings.sessions.revokeError'),
        kind: 'error',
        timeoutMs: 0,
      });
    }
  }

  async function revokeAllOther(): Promise<void> {
    const others = sessions.filter((s) => !s.is_current);
    if (others.length === 0) return;
    const ok = await confirm.ask({
      title: t('settings.sessions.revokeAll'),
      message: t('settings.sessions.revokeAllConfirm'),
      confirmLabel: t('settings.sessions.revokeAll'),
      cancelLabel: t('common.cancel'),
      kind: 'danger',
    });
    if (!ok) return;
    const errors: string[] = [];
    for (const s of others) {
      try {
        await del<void>(`/api/v1/auth/sessions/${s.session_id}`);
      } catch {
        errors.push(s.session_id);
      }
    }
    sessions = sessions.filter((s) => s.is_current || errors.includes(s.session_id));
    if (errors.length === 0) {
      toast.show({ message: t('settings.sessions.allRevoked'), timeoutMs: 4000 });
    } else {
      toast.show({
        message: t('settings.sessions.revokeError'),
        kind: 'error',
        timeoutMs: 0,
      });
    }
  }

  function formatDate(iso: string): string {
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

  /** Return a short device label from the user-agent string. */
  function deviceLabel(ua: string): string {
    if (!ua) return 'Unknown device';
    // Match common patterns.
    if (/iPhone/.test(ua)) return 'iPhone';
    if (/iPad/.test(ua)) return 'iPad';
    if (/Android/.test(ua)) return 'Android';
    if (/Macintosh/.test(ua)) return 'Mac';
    if (/Windows/.test(ua)) return 'Windows PC';
    if (/Linux/.test(ua)) return 'Linux';
    // Fall back to a truncated UA.
    return ua.length > 40 ? ua.slice(0, 40) + '...' : ua;
  }

  let hasOther = $derived(sessions.some((s) => !s.is_current));
</script>

{#if loading}
  <p aria-label={t('settings.sessions.loadingAria')} class="muted">{t('common.loading')}</p>
{:else if loadError}
  <p class="error">{loadError}</p>
{:else if sessions.length === 0}
  <p class="muted">{t('settings.sessions.empty')}</p>
{:else}
  <ul class="session-list">
    {#each sessions as session (session.session_id)}
      <li class="session-row" class:current={session.is_current}>
        <div class="session-info">
          <div class="session-device">
            {deviceLabel(session.user_agent)}
            {#if session.is_current}
              <span class="chip current-chip">{t('settings.sessions.thisSession')}</span>
            {/if}
          </div>
          <div class="session-meta">
            <span>{t('settings.sessions.ip')}: {session.last_seen_ip || '—'}</span>
            <span>{t('settings.sessions.createdAt')}: {formatDate(session.created_at)}</span>
            <span>{t('settings.sessions.lastSeen')}: {formatDate(session.last_seen_at)}</span>
          </div>
        </div>
        <div class="session-actions">
          {#if session.is_current}
            <Button variant="danger" onclick={() => revokeSession(session)}>
              {t('settings.sessions.signOut')}
            </Button>
          {:else}
            <Button variant="danger" onclick={() => revokeSession(session)}>
              {t('settings.sessions.revoke')}
            </Button>
          {/if}
        </div>
      </li>
    {/each}
  </ul>

  <div class="bulk-actions">
    <Button variant="danger" disabled={!hasOther} onclick={revokeAllOther}>
      {t('settings.sessions.revokeAll')}
    </Button>
  </div>
{/if}

<style>
  .session-list {
    list-style: none;
    padding: 0;
    margin: 0 0 var(--spacing-05) 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .session-row {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: var(--spacing-04);
    padding: var(--spacing-04);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    background: var(--layer-01);
  }

  .session-row.current {
    border-color: var(--interactive);
  }

  .session-info {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
    min-width: 0;
  }

  .session-device {
    font-weight: 600;
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    flex-wrap: wrap;
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

  .session-meta {
    display: flex;
    flex-wrap: wrap;
    gap: var(--spacing-03);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }

  .session-actions {
    flex-shrink: 0;
  }

  .bulk-actions {
    margin-top: var(--spacing-05);
  }

  .muted {
    color: var(--text-secondary);
  }

  .error {
    color: var(--support-error);
  }
</style>
