<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Label" {size}>
  <path d="M20 12l-8 8-9-9V3h8l9 9z" />
  <circle cx="7.5" cy="7.5" r="1.2" />
</Icon>
