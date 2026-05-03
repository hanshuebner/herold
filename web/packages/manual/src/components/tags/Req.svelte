<script lang="ts">
  import { defaultT } from '../../chrome/strings.js';

  interface Props {
    id: string;
    t?: (key: string) => string;
  }

  const { id, t = defaultT }: Props = $props();
</script>

<span class="req" title="{t('manual.req.label')}: {id}" aria-label="{t('manual.req.label')} {id}">
  <span class="req__badge">{id}</span>
</span>

<style>
  .req {
    display: inline-flex;
    align-items: center;
    vertical-align: middle;
  }

  .req__badge {
    display: inline-block;
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    line-height: var(--type-code-01-line);
    font-weight: 600;
    color: var(--support-info);
    background: color-mix(in srgb, var(--support-info) 12%, transparent);
    border: 1px solid color-mix(in srgb, var(--support-info) 30%, transparent);
    border-radius: var(--radius-sm);
    padding: 0 var(--spacing-02);
  }
</style>
