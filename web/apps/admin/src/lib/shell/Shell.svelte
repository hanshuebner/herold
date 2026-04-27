<script lang="ts">
  import { auth } from '../auth/auth.svelte';
  import { router } from '../router/router.svelte';

  interface NavItem {
    label: string;
    path: string;
    segment: string;
    soon?: boolean;
  }

  const navItems: NavItem[] = [
    { label: 'Dashboard', path: '/dashboard', segment: 'dashboard' },
    { label: 'Principals', path: '/principals', segment: 'principals' },
    { label: 'Domains', path: '/domains', segment: 'domains' },
    { label: 'Queue', path: '/queue', segment: 'queue' },
    { label: 'Audit', path: '/audit', segment: 'audit' },
    { label: 'Research', path: '/research', segment: 'research' },
  ];

  interface Props {
    children?: import('svelte').Snippet;
  }
  let { children }: Props = $props();
</script>

<div class="shell">
  <header class="topbar">
    <span class="wordmark">Herold admin</span>
    <div class="topbar-right">
      {#if auth.principal}
        <span class="principal-email">{auth.principal.email}</span>
        <button
          type="button"
          class="logout-btn"
          onclick={() => void auth.logout()}
        >
          Sign out
        </button>
      {/if}
    </div>
  </header>

  <div class="body">
    <nav class="rail" aria-label="Main navigation">
      <ul class="nav-list">
        {#each navItems as item (item.segment)}
          <li class:active={router.matches(item.segment)}>
            <button
              type="button"
              onclick={() => {
                if (!item.soon) router.navigate(item.path);
              }}
              aria-current={router.matches(item.segment) ? 'page' : undefined}
              class:nav-soon={item.soon}
              title={item.soon ? 'Coming soon' : undefined}
            >
              {item.label}
              {#if item.soon}
                <span class="soon-badge" aria-hidden="true">soon</span>
              {/if}
            </button>
          </li>
        {/each}
      </ul>
    </nav>

    <main class="content">
      {@render children?.()}
    </main>
  </div>
</div>

<style>
  .shell {
    display: flex;
    flex-direction: column;
    height: 100vh;
    height: 100dvh;
  }

  /* ---- Top bar ---- */
  .topbar {
    flex: 0 0 auto;
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0 var(--spacing-06);
    height: 48px;
    background: var(--layer-01);
    border-bottom: 1px solid var(--border-subtle-01);
  }
  .wordmark {
    font-family: var(--font-sans);
    font-size: var(--type-heading-compact-01-size);
    font-weight: var(--type-heading-compact-01-weight);
    color: var(--text-primary);
    letter-spacing: -0.01em;
  }
  .topbar-right {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
  }
  .principal-email {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }
  .logout-btn {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    padding: var(--spacing-02) var(--spacing-03);
    border-radius: var(--radius-md);
    background: none;
    border: none;
    cursor: pointer;
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
    min-height: var(--touch-min);
  }
  .logout-btn:hover {
    background: var(--layer-02);
    color: var(--text-primary);
  }

  /* ---- Body layout ---- */
  .body {
    flex: 1;
    display: flex;
    min-height: 0;
  }

  /* ---- Left rail nav ---- */
  .rail {
    flex: 0 0 200px;
    background: var(--layer-01);
    border-right: 1px solid var(--border-subtle-01);
    overflow-y: auto;
    padding: var(--spacing-04) 0;
  }
  .nav-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
  }
  .nav-list li {
    display: flex;
  }
  .nav-list li button {
    width: 100%;
    text-align: left;
    padding: var(--spacing-03) var(--spacing-05);
    font-size: var(--type-body-compact-01-size);
    line-height: var(--type-body-compact-01-line);
    color: var(--text-secondary);
    border-radius: var(--radius-md);
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter),
      color var(--duration-fast-02) var(--easing-productive-enter);
    background: none;
    border: none;
    cursor: pointer;
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .nav-list li button:hover:not(.nav-soon) {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .nav-list li.active button {
    background: var(--layer-02);
    color: var(--text-primary);
    font-weight: 600;
  }
  .nav-soon {
    opacity: 0.5;
    cursor: default !important;
  }
  .soon-badge {
    font-size: 10px;
    font-weight: 600;
    color: var(--text-helper);
    background: var(--layer-02);
    border-radius: var(--radius-pill);
    padding: 1px 6px;
    letter-spacing: 0.03em;
    text-transform: uppercase;
    display: none;
  }
  .nav-list li button.nav-soon:hover .soon-badge {
    display: inline-block;
  }

  /* ---- Main content ---- */
  .content {
    flex: 1;
    min-width: 0;
    overflow: auto;
    background: var(--background);
    padding: var(--spacing-06);
  }

  /* Narrow: collapse nav to top strip on very small viewports. */
  @media (max-width: 640px) {
    .rail {
      display: none;
    }
  }
</style>
