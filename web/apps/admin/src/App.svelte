<script lang="ts">
  import AuthGate from './lib/auth/AuthGate.svelte';
  import Shell from './lib/shell/Shell.svelte';
  import { router } from './lib/router/router.svelte';
  import { keyboard } from './lib/keyboard/engine.svelte';
  import DashboardView from './views/DashboardView.svelte';
  import NotFoundView from './views/NotFoundView.svelte';

  // Admin-global keyboard shortcuts.
  keyboard.registerGlobal({
    key: 'g d',
    description: 'Go to Dashboard',
    action: () => router.navigate('/dashboard'),
  });
</script>

<AuthGate>
  <Shell>
    {#if router.matches('dashboard')}
      <DashboardView />
    {:else}
      <NotFoundView />
    {/if}
  </Shell>
</AuthGate>
