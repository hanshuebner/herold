<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Add contact" {size}>
  <circle cx="9" cy="8" r="4" />
  <path d="M2 21a7 7 0 0 1 14 0" />
  <line x1="19" y1="6" x2="19" y2="14" />
  <line x1="15" y1="10" x2="23" y2="10" />
</Icon>
