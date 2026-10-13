<script lang="ts">
  /**
   * App-action icon (REQ-APPS-04). Renders one of the suite-owned icons
   * of the closed set; any other name renders the generic `app` icon.
   * Apps choose an icon by name only and never supply image data.
   */
  import Icon from '../icons/Icon.svelte';
  import { normalizeAppActionIcon } from './app-actions';

  interface Props {
    name?: string;
    size?: number;
  }
  let { name = 'app', size = 20 }: Props = $props();

  let icon = $derived(normalizeAppActionIcon(name));
</script>

<Icon label={icon} {size}>
  <g data-app-icon={icon}>
    {#if icon === 'gift'}
      <rect x="3" y="8" width="18" height="4" rx="1" />
      <path d="M5 12v8h14v-8" />
      <path d="M12 8v12" />
      <path d="M12 8c-1.5-3-5-3.5-5-1.5S10 8 12 8z" />
      <path d="M12 8c1.5-3 5-3.5 5-1.5S14 8 12 8z" />
    {:else if icon === 'ticket'}
      <path d="M3 8a1 1 0 011-1h16a1 1 0 011 1v2a2 2 0 000 4v2a1 1 0 01-1 1H4a1 1 0 01-1-1v-2a2 2 0 000-4z" />
      <path d="M14 7v2M14 11v2M14 15v2" />
    {:else if icon === 'calendar-plus'}
      <rect x="3" y="5" width="18" height="16" rx="2" />
      <path d="M3 10h18M8 3v4M16 3v4" />
      <path d="M12 13v5M9.5 15.5h5" />
    {:else if icon === 'user-plus'}
      <circle cx="9" cy="8" r="4" />
      <path d="M2 20c0-3.3 3.1-6 7-6s7 2.7 7 6" />
      <path d="M19 8v6M16 11h6" />
    {:else if icon === 'archive'}
      <rect x="2" y="4" width="20" height="4" rx="1" />
      <path d="M4 8v10a2 2 0 002 2h12a2 2 0 002-2V8" />
      <path d="M10 12h4" />
    {:else if icon === 'send'}
      <path d="M21 3L10 14" />
      <path d="M21 3l-7 18-4-7-7-4z" />
    {:else if icon === 'link'}
      <path d="M10 14a4 4 0 005.7 0l3-3a4 4 0 00-5.7-5.7l-1.2 1.2" />
      <path d="M14 10a4 4 0 00-5.7 0l-3 3a4 4 0 005.7 5.7l1.2-1.2" />
    {:else if icon === 'file-plus'}
      <path d="M14 3H6a2 2 0 00-2 2v14a2 2 0 002 2h12a2 2 0 002-2V9z" />
      <path d="M14 3v6h6" />
      <path d="M12 12v6M9 15h6" />
    {:else}
      <rect x="3" y="3" width="7" height="7" rx="1.5" />
      <rect x="14" y="3" width="7" height="7" rx="1.5" />
      <rect x="3" y="14" width="7" height="7" rx="1.5" />
      <rect x="14" y="14" width="7" height="7" rx="1.5" />
    {/if}
  </g>
</Icon>
