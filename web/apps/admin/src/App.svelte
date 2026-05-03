<script lang="ts">
  import AuthGate from './lib/auth/AuthGate.svelte';
  import Shell from './lib/shell/Shell.svelte';
  import { router } from './lib/router/router.svelte';
  import { keyboard } from './lib/keyboard/engine.svelte';
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

<AuthGate>
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
    {:else}
      <NotFoundView />
    {/if}
  </Shell>
</AuthGate>
