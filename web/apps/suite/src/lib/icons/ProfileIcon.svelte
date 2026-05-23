<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Account" {size}>
  <circle cx="12" cy="8" r="4" />
  <path d="M4 21a8 8 0 0 1 16 0" />
</Icon>
