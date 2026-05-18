<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Back" {size}>
  <path d="M15 6l-6 6 6 6" />
</Icon>
