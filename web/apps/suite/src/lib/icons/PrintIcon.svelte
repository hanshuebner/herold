<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Print" {size}>
  <path d="M6 8V3h12v5" />
  <rect x="3" y="8" width="18" height="9" rx="2" />
  <rect x="6" y="14" width="12" height="7" />
</Icon>
