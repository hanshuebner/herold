<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Block sender" {size}>
  <circle cx="12" cy="12" r="9" />
  <path d="M5.6 5.6l12.8 12.8" />
</Icon>
