<script lang="ts">
  import Icon from './Icon.svelte';
  interface Props {
    size?: number;
  }
  let { size = 20 }: Props = $props();
</script>

<Icon label="Settings" {size}>
  <circle cx="12" cy="12" r="3" />
  <path
    d="M19 12a7 7 0 0 0-.1-1.2l2.1-1.6-2-3.5-2.5.9a7 7 0 0 0-2-1.2L14 3h-4l-.5 2.4a7 7 0 0 0-2 1.2l-2.5-.9-2 3.5L5.1 10.8A7 7 0 0 0 5 12c0 .4 0 .8.1 1.2L3 14.8l2 3.5 2.5-.9c.6.5 1.3.9 2 1.2L10 21h4l.5-2.4a7 7 0 0 0 2-1.2l2.5.9 2-3.5-2.1-1.6c.1-.4.1-.8.1-1.2z"
  />
</Icon>
