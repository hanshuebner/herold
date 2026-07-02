<script lang="ts">
  import { untrack } from 'svelte';
  import AuthGate from './lib/auth/AuthGate.svelte';
  import Shell from './lib/shell/Shell.svelte';
  import { router } from './lib/router/router.svelte';
  import { keyboard } from './lib/keyboard/engine.svelte';
  import { watchRegistration, activateWaiting, startPeriodicUpdateCheck } from '@herold/clientlog/sw-update';
  import DashboardView from './views/DashboardView.svelte';
  import PrincipalsView from './views/PrincipalsView.svelte';
  import PrincipalDetailView from './views/PrincipalDetailView.svelte';
  import DomainsView from './views/DomainsView.svelte';
  import DomainDetailView from './views/DomainDetailView.svelte';
  import QueueView from './views/QueueView.svelte';
  import QueueItemView from './views/QueueItemView.svelte';
  import AuditView from './views/AuditView.svelte';
  import ClientlogView from './views/ClientlogView.svelte';
  import ResearchView from './views/ResearchView.svelte';
  import HelpView from './views/HelpView.svelte';
  import NotFoundView from './views/NotFoundView.svelte';

  // Admin-global keyboard shortcuts.
  keyboard.registerGlobal({
    key: 'g d',
    description: 'Go to Dashboard',
    action: () => router.navigate('/dashboard'),
  });
  keyboard.registerGlobal({
    key: 'g p',
    description: 'Go to Principals',
    action: () => router.navigate('/principals'),
  });
  keyboard.registerGlobal({
    key: 'g o',
    description: 'Go to Domains',
    action: () => router.navigate('/domains'),
  });
  keyboard.registerGlobal({
    key: 'g q',
    description: 'Go to Queue',
    action: () => router.navigate('/queue'),
  });
  keyboard.registerGlobal({
    key: 'g a',
    description: 'Go to Audit',
    action: () => router.navigate('/audit'),
  });
  keyboard.registerGlobal({
    key: 'g l',
    description: 'Go to Client logs',
    action: () => router.navigate('/clientlog'),
  });
  keyboard.registerGlobal({
    key: 'g r',
    description: 'Go to Research',
    action: () => router.navigate('/research'),
  });
  keyboard.registerGlobal({
    key: 'g h',
    description: 'Go to Help',
    action: () => router.navigate('/help'),
  });

  // ── SW update notification (REQ-PUSH-72 / REQ-MOB-75) ───────────────────
  //
  // The admin SPA registers a minimal service worker whose only job is to
  // relay the install -> WAITING -> activating lifecycle.  watchRegistration()
  // shows the banner while the user is still on the OLD version.  Clicking
  // Reload posts SKIP_WAITING, the new worker activates, and exactly one
  // location.reload() follows.  The periodic check fires reg.update() every
  // hour so a long-lived admin session is notified within an hour of a new
  // deploy, without depending on browser navigation events.

  let showSwUpdateBanner = $state(false);
  // Holds the registration for use in reloadAfterSwUpdate().
  let swRegistration: ServiceWorkerRegistration | null = null;

  $effect(() => {
    if (!('serviceWorker' in navigator)) return;
    // The admin SW is registered at /admin/sw.js with scope /admin/ so it
    // does not interfere with the suite SW at / (scope /).
    void navigator.serviceWorker
      .register('/admin/sw.js', { scope: '/admin/' })
      .then((reg) => {
        swRegistration = reg;
        // One-shot: right after a user-accepted update reload we set a session
        // flag so the version we just activated is not re-announced as "new".
        let suppressInitialWaiting = false;
        try {
          if (sessionStorage.getItem('herold-admin-sw-reloaded') === '1') {
            sessionStorage.removeItem('herold-admin-sw-reloaded');
            suppressInitialWaiting = true;
          }
        } catch {
          // sessionStorage unavailable (private mode / sandbox) — ignore.
        }
        watchRegistration(
          reg,
          () => navigator.serviceWorker.controller,
          () => untrack(() => { showSwUpdateBanner = true; }),
          { suppressInitialWaiting },
        );
        // Poll for an updated sw.js every hour so the banner appears while
        // the admin user is still on the old version (REQ-MOB-75).
        startPeriodicUpdateCheck(reg, 60 * 60 * 1000);
      })
      .catch(() => {
        // SW registration failed (e.g. insecure context in dev) — update
        // detection is unavailable, which is acceptable in that environment.
      });
  });

  function reloadAfterSwUpdate(): void {
    showSwUpdateBanner = false;
    activateWaiting(
      swRegistration?.waiting ?? null,
      (cb) => navigator.serviceWorker.addEventListener('controllerchange', cb),
      () => {
        try {
          sessionStorage.setItem('herold-admin-sw-reloaded', '1');
        } catch {
          // sessionStorage unavailable — the worst case is a single re-prompt.
        }
        location.reload();
      },
    );
  }

  /** The principal ID segment for /principals/:id routes. */
  const principalId = $derived(
    router.matches('principals') && router.parts.length >= 2
      ? (router.parts[1] ?? null)
      : null,
  );

  /** The domain name segment for /domains/:name routes. */
  const domainName = $derived(
    router.matches('domains') && router.parts.length >= 2
      ? (router.parts[1] ?? null)
      : null,
  );

  /** The queue item ID segment for /queue/:id routes. */
  const queueId = $derived(
    router.matches('queue') && router.parts.length >= 2
      ? (router.parts[1] ?? null)
      : null,
  );
</script>

<style>
  .sw-update-banner {
    position: fixed;
    bottom: var(--spacing-05);
    left: 50%;
    transform: translateX(-50%);
    z-index: 500;
    background: var(--layer-03);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-03) var(--spacing-05);
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    box-shadow: 0 4px 16px rgba(0, 0, 0, 0.24);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    max-width: 480px;
    width: calc(100vw - var(--spacing-06) * 2);
  }

  .sw-update-banner span {
    flex: 1;
  }

  .sw-update-banner button:last-child {
    width: 28px;
    height: 28px;
    border-radius: var(--radius-md);
    color: var(--text-secondary);
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    background: none;
    border: none;
    cursor: pointer;
  }

  .sw-update-banner button:last-child:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  .sw-update-banner button:not(:last-child) {
    padding: var(--spacing-02) var(--spacing-04);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    border: none;
    font-weight: 600;
    min-height: var(--touch-min);
    cursor: pointer;
  }

  .sw-update-banner button:not(:last-child):hover {
    filter: brightness(1.1);
  }
</style>

<AuthGate>
<!-- SW update banner (REQ-PUSH-72 / REQ-MOB-75). -->
{#if showSwUpdateBanner}
  <div class="sw-update-banner" role="status" aria-live="polite">
    <span>A new version is available.</span>
    <button type="button" onclick={reloadAfterSwUpdate}>Reload</button>
    <button type="button" aria-label="Dismiss" onclick={() => (showSwUpdateBanner = false)}>
      &#10005;
    </button>
  </div>
{/if}
  <Shell>
    {#if router.matches('dashboard')}
      <DashboardView />
    {:else if router.matches('principals') && principalId !== null}
      <PrincipalDetailView id={principalId} />
    {:else if router.matches('principals')}
      <PrincipalsView />
    {:else if router.matches('domains') && domainName !== null}
      <DomainDetailView name={domainName} />
    {:else if router.matches('domains')}
      <DomainsView />
    {:else if router.matches('queue') && queueId !== null}
      <QueueItemView id={queueId} />
    {:else if router.matches('queue')}
      <QueueView />
    {:else if router.matches('audit')}
      <AuditView />
    {:else if router.matches('clientlog')}
      <ClientlogView />
    {:else if router.matches('research')}
      <ResearchView />
    {:else if router.matches('help')}
      <HelpView />
    {:else}
      <NotFoundView />
    {/if}
  </Shell>
</AuthGate>
