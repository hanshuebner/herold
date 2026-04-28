<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Mute thread" {size}>
  <path d="M11 5L6 9H3v6h3l5 4V5z" />
  <path d="M16 8l5 8" />
  <path d="M21 8l-5 8" />
</Icon>
