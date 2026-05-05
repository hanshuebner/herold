<script lang="ts">
  /**
   * Settings section for configuring message and thread action toolbars
   * (re #60). Lets the user:
   *   - Drag-to-reorder actions within each scope
   *   - Toggle which actions are primary (shown in toolbar) vs overflow-only
   *   - Adjust the visible count via a spinner
   *   - Reset either scope to defaults
   *
   * State is backed by messageActionsPrefs (localStorage; see the TODO in
   * that module about migrating to server-persisted storage).
   */
  import { messageActionsPrefs } from '../../lib/mail/messageActionsPrefs.svelte';
  import { MESSAGE_ACTIONS, THREAD_ACTIONS } from '../../lib/mail/actions';
  import { t } from '../../lib/i18n/i18n.svelte';

  // ── Drag-to-reorder state ─────────────────────────────────────────────────

  let dragging = $state<{ scope: 'message' | 'thread'; fromIndex: number } | null>(null);
  let dragOverIndex = $state<number | null>(null);

  function startDrag(scope: 'message' | 'thread', fromIndex: number): void {
    dragging = { scope, fromIndex };
    dragOverIndex = null;
  }

  function onDragOver(e: DragEvent, toIndex: number): void {
    e.preventDefault();
    dragOverIndex = toIndex;
  }

  function onDrop(scope: 'message' | 'thread', toIndex: number): void {
    if (!dragging || dragging.scope !== scope) return;
    if (dragging.fromIndex !== toIndex) {
      messageActionsPrefs.reorder(scope, dragging.fromIndex, toIndex);
    }
    dragging = null;
    dragOverIndex = null;
  }

  function onDragEnd(): void {
    dragging = null;
    dragOverIndex = null;
  }

  // ── Keyboard-driven reorder (accessibility fallback) ──────────────────────

  function moveUp(scope: 'message' | 'thread', index: number): void {
    if (index === 0) return;
    messageActionsPrefs.reorder(scope, index, index - 1);
  }

  function moveDown(scope: 'message' | 'thread', index: number, total: number): void {
    if (index === total - 1) return;
    messageActionsPrefs.reorder(scope, index, index + 1);
  }

  // ── Helpers ───────────────────────────────────────────────────────────────

  function labelFor(scope: 'message' | 'thread', id: string): string {
    const registry = scope === 'message' ? MESSAGE_ACTIONS : THREAD_ACTIONS;
    const def = registry.find((a) => a.id === id);
    if (!def) return id;
    return t(def.labelKey as Parameters<typeof t>[0]);
  }
</script>

<div class="msg-actions-settings">
  <p class="hint">{t('settings.messageActions.hint')}</p>

  {#each (['message', 'thread'] as const) as scope (scope)}
    {@const prefs = scope === 'message' ? messageActionsPrefs.message : messageActionsPrefs.thread}
    {@const heading = scope === 'message'
      ? t('settings.messageActions.perMessage')
      : t('settings.messageActions.perThread')}

    <section class="scope-section">
      <div class="scope-header">
        <h3>{heading}</h3>
        <button
          type="button"
          class="reset-btn"
          onclick={() => messageActionsPrefs.resetToDefaults(scope)}
        >
          {t('settings.messageActions.resetDefaults')}
        </button>
      </div>

      <div class="visible-count-row">
        <label for="visible-count-{scope}">
          {t('settings.messageActions.visibleCount').replace('{count}', String(prefs.visibleCount))}
        </label>
        <input
          id="visible-count-{scope}"
          type="number"
          min="1"
          max={prefs.order.length}
          value={prefs.visibleCount}
          oninput={(e) => {
            const v = parseInt((e.currentTarget as HTMLInputElement).value, 10);
            if (!isNaN(v) && v >= 1 && v <= prefs.order.length) {
              messageActionsPrefs.setVisibleCount(scope, v);
            }
          }}
          class="count-input"
          aria-label="Number of primary actions"
        />
      </div>

      <ul class="action-list" role="list">
        {#each prefs.order as id, i (id)}
          {@const isPrimary = i < prefs.visibleCount}
          {@const label = labelFor(scope, id)}
          <li
            class="action-row"
            class:primary={isPrimary}
            class:overflow={!isPrimary}
            class:drag-over={dragging?.scope === scope && dragOverIndex === i}
            draggable="true"
            role="listitem"
            ondragstart={() => startDrag(scope, i)}
            ondragover={(e) => onDragOver(e, i)}
            ondrop={() => onDrop(scope, i)}
            ondragend={onDragEnd}
          >
            <span class="drag-handle" aria-hidden="true" title="Drag to reorder">
              &#8942;&#8942;
            </span>

            <span class="action-label">{label}</span>

            <span class="position-badge" aria-label={isPrimary ? 'In toolbar' : 'In overflow menu'}>
              {isPrimary ? t('settings.messageActions.inToolbar') : t('actions.moreActions')}
            </span>

            <label class="toggle" aria-label="{t('settings.messageActions.inToolbar')}: {label}">
              <input
                type="checkbox"
                checked={isPrimary}
                onchange={() => messageActionsPrefs.toggleVisible(scope, id)}
              />
              <span class="track" aria-hidden="true"></span>
            </label>

            <span class="reorder-btns" aria-label="Reorder {label}">
              <button
                type="button"
                class="reorder-btn"
                onclick={() => moveUp(scope, i)}
                disabled={i === 0}
                aria-label="Move {label} up"
                title="Move up"
              >
                &#8593;
              </button>
              <button
                type="button"
                class="reorder-btn"
                onclick={() => moveDown(scope, i, prefs.order.length)}
                disabled={i === prefs.order.length - 1}
                aria-label="Move {label} down"
                title="Move down"
              >
                &#8595;
              </button>
            </span>
          </li>
        {/each}
      </ul>
    </section>
  {/each}
</div>

<style>
  .msg-actions-settings {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-06);
  }

  .hint {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  .scope-section {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
  }

  .scope-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-04);
  }

  .scope-header h3 {
    font-size: var(--type-heading-compact-02-size);
    line-height: var(--type-heading-compact-02-line);
    font-weight: var(--type-heading-compact-02-weight);
    color: var(--text-secondary);
    margin: 0;
  }

  .reset-btn {
    font-size: var(--type-body-compact-01-size);
    color: var(--interactive);
    padding: var(--spacing-01) var(--spacing-03);
    border-radius: var(--radius-md);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .reset-btn:hover {
    background: var(--layer-01);
    text-decoration: underline;
  }

  .visible-count-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
  }

  .count-input {
    width: 4em;
    padding: var(--spacing-01) var(--spacing-02);
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    text-align: center;
  }

  .action-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    overflow: hidden;
  }

  .action-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    padding: var(--spacing-03) var(--spacing-04);
    background: var(--layer-01);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    cursor: grab;
    user-select: none;
  }

  .action-row:active {
    cursor: grabbing;
  }

  .action-row.overflow {
    background: var(--background);
    opacity: 0.75;
  }

  .action-row.drag-over {
    background: color-mix(in srgb, var(--interactive) 10%, var(--layer-01));
    border-left: 2px solid var(--interactive);
  }

  .drag-handle {
    color: var(--text-helper);
    font-size: 12px;
    letter-spacing: -2px;
    flex-shrink: 0;
    cursor: grab;
  }

  .action-label {
    flex: 1;
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .position-badge {
    font-size: 11px;
    color: var(--text-helper);
    white-space: nowrap;
    flex-shrink: 0;
  }

  .toggle {
    position: relative;
    display: inline-flex;
    width: 36px;
    height: 20px;
    cursor: pointer;
    flex-shrink: 0;
  }
  .toggle input {
    position: absolute;
    inset: 0;
    opacity: 0;
    width: 100%;
    height: 100%;
    margin: 0;
    cursor: pointer;
  }
  .toggle .track {
    width: 100%;
    height: 100%;
    background: var(--layer-02);
    border-radius: var(--radius-pill);
    position: relative;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .toggle .track::before {
    content: '';
    position: absolute;
    top: 2px;
    left: 2px;
    width: 16px;
    height: 16px;
    background: var(--text-on-color);
    border-radius: var(--radius-pill);
    transition: transform var(--duration-fast-02) var(--easing-productive-enter);
  }
  .toggle input:checked + .track {
    background: var(--interactive);
  }
  .toggle input:checked + .track::before {
    transform: translateX(16px);
  }

  .reorder-btns {
    display: inline-flex;
    gap: 2px;
    flex-shrink: 0;
  }

  .reorder-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    border-radius: var(--radius-sm);
    color: var(--text-secondary);
    font-size: 12px;
    background: transparent;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .reorder-btn:hover:not(:disabled) {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .reorder-btn:disabled {
    opacity: 0.3;
    cursor: not-allowed;
  }
</style>
