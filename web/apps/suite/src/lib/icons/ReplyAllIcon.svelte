<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Reply all" {size}>
  <path d="M7 17l-5-5 5-5" />
  <path d="M12 17l-5-5 5-5" />
  <path d="M7 12h7a6 6 0 016 6v2" />
</Icon>
