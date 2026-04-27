<script lang="ts">
  // Bottom-anchored strip per docs/requirements/23-shortcut-coach.md.
  // Currently always-empty; the coach engine lands later.
</script>

<aside
  class="coach-strip"
  role="status"
  aria-live="polite"
  aria-label="Keyboard-shortcut hint"
>
  <!-- empty when no hint is active -->
</aside>

<style>
  .coach-strip {
    height: 24px;
    background: var(--background);
    border-top: 1px solid var(--border-subtle-01);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 0 var(--spacing-05);
    flex: 0 0 auto;
  }
</style>
