<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Reply" {size}>
  <path d="M9 17l-6-6 6-6" />
  <path d="M3 11h11a6 6 0 016 6v2" />
</Icon>
