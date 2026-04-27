<script lang="ts">
  import AuthGate from './lib/auth/AuthGate.svelte';
  import Shell from './lib/shell/Shell.svelte';
  import { router } from './lib/router/router.svelte';
  import { keyboard } from './lib/keyboard/engine.svelte';
  import DashboardView from './views/DashboardView.svelte';
  import PrincipalsView from './views/PrincipalsView.svelte';
  import PrincipalDetailView from './views/PrincipalDetailView.svelte';
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

  /** The principal ID segment for /principals/:id routes. */
  const principalId = $derived(
    router.matches('principals') && router.parts.length >= 2
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
    {:else}
      <NotFoundView />
    {/if}
  </Shell>
</AuthGate>
