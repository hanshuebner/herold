<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Admin" {size}>
  <path d="M12 2l2.4 4.8L20 8l-4 3.9.9 5.5L12 15l-4.9 2.4.9-5.5L4 8l5.6-1.2z" />
</Icon>
