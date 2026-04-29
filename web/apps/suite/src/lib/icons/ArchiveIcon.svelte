<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Archive" {size}>
  <rect x="2" y="4" width="20" height="4" rx="1" />
  <path d="M4 8v10a2 2 0 002 2h12a2 2 0 002-2V8" />
  <path d="M10 12h4" />
</Icon>
