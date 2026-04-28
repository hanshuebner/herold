<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Open menu" {size}>
  <path d="M6 9l6 6 6-6" />
</Icon>
