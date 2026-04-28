<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Report phishing" {size}>
  <path d="M12 3a9 9 0 019 9c0 5-9 9-9 9s-9-4-9-9a9 9 0 019-9z" />
  <path d="M9 9l6 6" />
  <path d="M15 9l-6 6" />
</Icon>
