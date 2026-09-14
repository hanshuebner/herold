<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Why was this classified?" {size}>
  <circle cx="12" cy="12" r="9" />
  <line x1="12" y1="11" x2="12" y2="16.5" />
  <circle cx="12" cy="7.5" r="0.75" fill="currentColor" stroke="none" />
</Icon>
