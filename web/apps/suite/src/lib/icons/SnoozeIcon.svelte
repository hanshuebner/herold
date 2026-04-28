<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Snooze" {size}>
  <circle cx="12" cy="13" r="8" />
  <path d="M12 9v4l3 2" />
  <path d="M3 5l4-3" />
  <path d="M21 5l-4-3" />
</Icon>
