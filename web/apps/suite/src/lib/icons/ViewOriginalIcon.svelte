<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="View original" {size}>
  <path d="M14 3v4a1 1 0 0 0 1 1h4" />
  <path d="M17 21H7a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h7l5 5v11a2 2 0 0 1-2 2z" />
  <polyline points="10 13 8 15 10 17" />
  <polyline points="14 13 16 15 14 17" />
</Icon>
