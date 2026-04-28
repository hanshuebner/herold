<script lang="ts">
  /**
   * Structured filter editor — Wave 3.15, REQ-FLT-01..31.
   *
   * Sections:
   *   1. Managed rules list: each rule with name, enabled toggle, conditions
   *      summary, actions summary, up/down reorder, edit, delete.
   *   2. Create / edit rule modal (inline below the list).
   *   3. Blocked senders subsection (rules matching the blocked-sender shape).
   *
   * REQ-FLT-30: the editor does not expose Sieve syntax. The raw Sieve
   * editor (SieveForm.svelte) coexists and is accessible from the Mail
   * settings section. This component only shows structured ManagedRules.
   */
  import { untrack } from 'svelte';
  import {
    managedRules,
    hasDeleteApplyLabelConflict,
    isBlockedSenderRule,
    blockedSenderAddress,
    type ManagedRule,
    type RuleCondition,
    type RuleAction,
    type ConditionField,
    type ConditionOp,
    type ActionKind,
  } from '../../lib/settings/managed-rules.svelte';
  import { filterLike } from '../../lib/settings/filter-like.svelte';

  $effect(() => {
    if (managedRules.loadStatus === 'idle') {
      untrack(() => {
        void managedRules.load();
      });
    }
  });

  // Consume a pending "Filter messages like this" payload from MessageAccordion.
  // Reading filterLike.pending registers it as a dependency so this effect
  // re-runs when MessageAccordion sets a new payload.
  $effect(() => {
    if (filterLike.pending) {
      const payload = untrack(() => filterLike.consume());
      if (payload) {
        untrack(() => openCreate(payload));
      }
    }
  });

  // ── Editor state ─────────────────────────────────────────────────────────

  type EditorMode = 'none' | 'create' | 'edit';
  let editorMode = $state<EditorMode>('none');
  let editingRuleId = $state<string | null>(null);

  // Editable fields in the current rule form.
  let editName = $state('');
  let editEnabled = $state(true);
  let editConditions = $state<RuleCondition[]>([{ field: 'from', op: 'contains', value: '' }]);
  let editActions = $state<RuleAction[]>([{ kind: 'skip-inbox' }]);

  // Validation error string, shown inline in the editor.
  let validationError = $state<string | null>(null);

  // "Test against existing mail" state.
  let testCount = $state<number | null>(null);
  let testRunning = $state(false);

  // Saving/deleting in-progress state.
  let saving = $state(false);
  let deletingId = $state<string | null>(null);

  // ── Pre-populate support ─────────────────────────────────────────────────

  // External callers (MessageAccordion "Filter messages like this") can set
  // this to pre-populate the editor with derived conditions.
  interface PrePopulatePayload {
    conditions: RuleCondition[];
  }

  // Expose a function to pre-populate the form. Components that use this
  // component can call filtersForm.openCreate(payload).
  export function openCreate(payload?: PrePopulatePayload): void {
    editName = '';
    editEnabled = true;
    editConditions = payload?.conditions?.length
      ? payload.conditions.map((c) => ({ ...c }))
      : [{ field: 'from', op: 'contains', value: '' }];
    editActions = [{ kind: 'skip-inbox' }];
    validationError = null;
    testCount = null;
    editingRuleId = null;
    editorMode = 'create';
  }

  function openEdit(rule: ManagedRule): void {
    editName = rule.name;
    editEnabled = rule.enabled;
    editConditions = rule.conditions.map((c) => ({ ...c }));
    editActions = rule.actions.map((a) => ({ ...a }));
    validationError = null;
    testCount = null;
    editingRuleId = rule.id;
    editorMode = 'edit';
  }

  function cancelEditor(): void {
    editorMode = 'none';
    editingRuleId = null;
    testCount = null;
    validationError = null;
  }

  // ── Conditions ───────────────────────────────────────────────────────────

  const CONDITION_FIELDS: { value: ConditionField; label: string }[] = [
    { value: 'from', label: 'From' },
    { value: 'to', label: 'To' },
    { value: 'subject', label: 'Subject' },
    { value: 'from-domain', label: 'From domain' },
    { value: 'has-attachment', label: 'Has attachment' },
    { value: 'thread-id', label: 'Thread ID' },
  ];

  const CONDITION_OPS: { value: ConditionOp; label: string }[] = [
    { value: 'contains', label: 'contains' },
    { value: 'equals', label: 'equals' },
    { value: 'wildcard-match', label: 'matches wildcard' },
  ];

  const ACTION_KINDS: { value: ActionKind; label: string }[] = [
    { value: 'skip-inbox', label: 'Skip inbox (archive on arrival)' },
    { value: 'mark-read', label: 'Mark as read' },
    { value: 'apply-label', label: 'Apply label' },
    { value: 'delete', label: 'Move to Trash' },
    { value: 'forward', label: 'Forward to address' },
  ];

  function addCondition(): void {
    editConditions = [...editConditions, { field: 'from', op: 'contains', value: '' }];
  }

  function removeCondition(i: number): void {
    editConditions = editConditions.filter((_, idx) => idx !== i);
  }

  function setConditionField(i: number, field: ConditionField): void {
    editConditions = editConditions.map((c, idx) =>
      idx === i ? { ...c, field } : c,
    );
  }

  function setConditionOp(i: number, op: ConditionOp): void {
    editConditions = editConditions.map((c, idx) =>
      idx === i ? { ...c, op } : c,
    );
  }

  function setConditionValue(i: number, value: string): void {
    editConditions = editConditions.map((c, idx) =>
      idx === i ? { ...c, value } : c,
    );
  }

  // ── Actions ──────────────────────────────────────────────────────────────

  function addAction(): void {
    editActions = [...editActions, { kind: 'skip-inbox' }];
  }

  function removeAction(i: number): void {
    editActions = editActions.filter((_, idx) => idx !== i);
  }

  function setActionKind(i: number, kind: ActionKind): void {
    const params = kind === 'apply-label' ? { label: '' } : kind === 'forward' ? { to: '' } : undefined;
    editActions = editActions.map((a, idx) =>
      idx === i ? { kind, params } : a,
    );
  }

  function setActionParam(i: number, key: string, value: string): void {
    editActions = editActions.map((a, idx) => {
      if (idx !== i) return a;
      return { ...a, params: { ...(a.params ?? {}), [key]: value } };
    });
  }

  // ── Validation ────────────────────────────────────────────────────────────

  function validate(): boolean {
    if (editConditions.length === 0) {
      validationError = 'At least one condition is required.';
      return false;
    }
    if (editActions.length === 0) {
      validationError = 'At least one action is required.';
      return false;
    }
    if (hasDeleteApplyLabelConflict(editActions)) {
      validationError =
        '"Move to Trash" and "Apply label" cannot be combined: the delete action short-circuits and the label is never applied.';
      return false;
    }
    for (const c of editConditions) {
      if (c.field !== 'has-attachment' && !c.value.trim()) {
        validationError = 'All condition values must be non-empty.';
        return false;
      }
    }
    for (const a of editActions) {
      if (a.kind === 'forward' && !String(a.params?.to ?? '').trim()) {
        validationError = 'Forward address is required.';
        return false;
      }
      if (a.kind === 'apply-label' && !String(a.params?.label ?? '').trim()) {
        validationError = 'Label name is required for Apply label action.';
        return false;
      }
    }
    validationError = null;
    return true;
  }

  // ── Save ──────────────────────────────────────────────────────────────────

  async function saveRule(): Promise<void> {
    if (!validate()) return;
    saving = true;
    try {
      const maxOrder = managedRules.rules.reduce((m, r) => Math.max(m, r.order), -1);
      const payload = {
        name: editName.trim() || '',
        enabled: editEnabled,
        order: editorMode === 'edit'
          ? (managedRules.rules.find((r) => r.id === editingRuleId)?.order ?? maxOrder + 1)
          : maxOrder + 1,
        conditions: editConditions,
        actions: editActions,
      };

      if (editorMode === 'create') {
        const created = await managedRules.create(payload);
        if (created) {
          cancelEditor();
          toast.show({ message: 'Filter created' });
        }
      } else if (editorMode === 'edit' && editingRuleId) {
        const ok = await managedRules.update(editingRuleId, payload);
        if (ok) {
          cancelEditor();
          toast.show({ message: 'Filter updated' });
        }
      }
    } finally {
      saving = false;
    }
  }

  // ── Test against existing mail ─────────────────────────────────────────

  async function runTest(): Promise<void> {
    testRunning = true;
    testCount = null;
    try {
      const count = await managedRules.testFilter(editConditions);
      testCount = count;
    } catch {
      testCount = null;
    } finally {
      testRunning = false;
    }
  }

  // ── Reorder ──────────────────────────────────────────────────────────────

  // Only reorders rules that are NOT blocked-sender rules (those are
  // managed separately in the blocked-senders section).
  let filterRules = $derived(managedRules.rules.filter((r) => !isBlockedSenderRule(r)));
  let blockedSenderRules = $derived(managedRules.rules.filter((r) => isBlockedSenderRule(r)));

  async function moveRuleUp(rule: ManagedRule): Promise<void> {
    const idx = filterRules.findIndex((r) => r.id === rule.id);
    if (idx <= 0) return;
    const prev = filterRules[idx - 1]!;
    // Swap orders.
    await managedRules.setOrder(rule.id, prev.order);
    await managedRules.setOrder(prev.id, rule.order);
  }

  async function moveRuleDown(rule: ManagedRule): Promise<void> {
    const idx = filterRules.findIndex((r) => r.id === rule.id);
    if (idx < 0 || idx >= filterRules.length - 1) return;
    const next = filterRules[idx + 1]!;
    await managedRules.setOrder(rule.id, next.order);
    await managedRules.setOrder(next.id, rule.order);
  }

  // ── Delete confirmation ───────────────────────────────────────────────────

  let deleteConfirmId = $state<string | null>(null);

  function requestDelete(id: string): void {
    deleteConfirmId = id;
  }

  async function confirmDelete(): Promise<void> {
    const id = deleteConfirmId;
    if (!id) return;
    deleteConfirmId = null;
    deletingId = id;
    try {
      await managedRules.delete(id);
    } finally {
      deletingId = null;
    }
  }

  function cancelDelete(): void {
    deleteConfirmId = null;
  }

  // ── Summary helpers ───────────────────────────────────────────────────────

  function conditionSummary(conditions: RuleCondition[]): string {
    if (conditions.length === 0) return '(no conditions)';
    return conditions
      .map((c) => {
        if (c.field === 'has-attachment') return 'has attachment';
        const fieldLabel =
          CONDITION_FIELDS.find((f) => f.value === c.field)?.label ?? c.field;
        return `${fieldLabel} ${c.op} "${c.value}"`;
      })
      .join(', ');
  }

  function actionSummary(actions: RuleAction[]): string {
    if (actions.length === 0) return '(no actions)';
    return actions
      .map((a) => {
        const label = ACTION_KINDS.find((k) => k.value === a.kind)?.label ?? a.kind;
        if (a.kind === 'apply-label') return `Apply label: ${a.params?.label ?? '?'}`;
        if (a.kind === 'forward') return `Forward to: ${a.params?.to ?? '?'}`;
        return label;
      })
      .join(', ');
  }

  // import toast separately so we can call it in saveRule (already in store)
  import { toast } from '../../lib/toast/toast.svelte';
</script>

{#if managedRules.loadStatus === 'loading' || managedRules.loadStatus === 'idle'}
  <p class="hint">Loading filters…</p>
{:else if managedRules.loadStatus === 'error'}
  <p class="error" role="alert">{managedRules.loadError}</p>
  <button type="button" onclick={() => void managedRules.load(true)}>Retry</button>
{:else}
  <!-- ── Filters list ─────────────────────────────────────────────────── -->

  <section class="form-section">
    <div class="section-header">
      <h3>Filters</h3>
      <button type="button" class="small-btn" onclick={() => openCreate()}>
        Create filter
      </button>
    </div>

    <p class="hint">
      Filters run on incoming mail in order (lowest order first). AND combines
      multiple conditions. The raw Sieve editor in "Mail" settings works
      alongside these structured filters; both sources coexist without
      interfering.
    </p>

    {#if filterRules.length === 0}
      <p class="muted">No filters yet.</p>
    {:else}
      <ul class="rule-list">
        {#each filterRules as rule, i (rule.id)}
          <li class="rule-row" class:disabled={!rule.enabled}>
            <div class="rule-order-btns">
              <button
                type="button"
                class="icon-btn"
                aria-label="Move filter up"
                disabled={i === 0}
                onclick={() => void moveRuleUp(rule)}
              >
                &#8593;
              </button>
              <button
                type="button"
                class="icon-btn"
                aria-label="Move filter down"
                disabled={i === filterRules.length - 1}
                onclick={() => void moveRuleDown(rule)}
              >
                &#8595;
              </button>
            </div>

            <label class="switch" title={rule.enabled ? 'Enabled' : 'Disabled'}>
              <input
                type="checkbox"
                checked={rule.enabled}
                onchange={(e) =>
                  void managedRules.setEnabled(
                    rule.id,
                    (e.currentTarget as HTMLInputElement).checked,
                  )}
              />
              <span class="track" aria-hidden="true"></span>
            </label>

            <div class="rule-summary">
              <span class="rule-name">{rule.name || '(unnamed)'}</span>
              <span class="rule-detail">
                If {conditionSummary(rule.conditions)} &rarr; {actionSummary(rule.actions)}
              </span>
            </div>

            <div class="rule-actions">
              <button
                type="button"
                class="small-btn"
                onclick={() => openEdit(rule)}
              >
                Edit
              </button>
              {#if deleteConfirmId === rule.id}
                <span class="confirm-delete">
                  Delete this filter?
                  <button type="button" class="small-btn danger" onclick={() => void confirmDelete()}>
                    Delete
                  </button>
                  <button type="button" class="small-btn" onclick={cancelDelete}>
                    Cancel
                  </button>
                </span>
              {:else}
                <button
                  type="button"
                  class="icon-btn danger"
                  aria-label="Delete filter"
                  disabled={deletingId === rule.id}
                  onclick={() => requestDelete(rule.id)}
                >
                  &#10005;
                </button>
              {/if}
            </div>
          </li>
        {/each}
      </ul>
    {/if}
  </section>

  <!-- ── Rule editor ────────────────────────────────────────────────────── -->

  {#if editorMode !== 'none'}
    <section class="form-section editor">
      <h3>{editorMode === 'create' ? 'Create filter' : 'Edit filter'}</h3>

      <div class="field-row">
        <label>
          <span class="field-label">Name <span class="muted">(optional)</span></span>
          <input
            type="text"
            bind:value={editName}
            placeholder="My filter"
            autocomplete="off"
          />
        </label>
        <label class="inline-check">
          <input type="checkbox" bind:checked={editEnabled} />
          <span>Enabled</span>
        </label>
      </div>

      <!-- Conditions -->
      <div class="subsection">
        <div class="subsection-header">
          <span class="subsection-title">Conditions (AND combined)</span>
          <button type="button" class="small-btn" onclick={addCondition}>
            Add condition
          </button>
        </div>

        {#if editConditions.length === 0}
          <p class="muted">No conditions — matches all mail.</p>
        {:else}
          <ul class="cond-list">
            {#each editConditions as cond, i (i)}
              <li class="cond-row">
                <select
                  value={cond.field}
                  onchange={(e) =>
                    setConditionField(i, (e.currentTarget as HTMLSelectElement).value as ConditionField)}
                  aria-label="Condition field"
                >
                  {#each CONDITION_FIELDS as f (f.value)}
                    <option value={f.value}>{f.label}</option>
                  {/each}
                </select>

                {#if cond.field !== 'has-attachment'}
                  <select
                    value={cond.op}
                    onchange={(e) =>
                      setConditionOp(i, (e.currentTarget as HTMLSelectElement).value as ConditionOp)}
                    aria-label="Condition operator"
                  >
                    {#each CONDITION_OPS as op (op.value)}
                      <option value={op.value}>{op.label}</option>
                    {/each}
                  </select>

                  <input
                    type="text"
                    value={cond.value}
                    oninput={(e) =>
                      setConditionValue(i, (e.currentTarget as HTMLInputElement).value)}
                    placeholder={cond.field === 'from' ? 'user@example.com or *@example.com' : ''}
                    aria-label="Condition value"
                  />
                {:else}
                  <span class="cond-bool">(boolean — matches messages with attachments)</span>
                {/if}

                <button
                  type="button"
                  class="icon-btn danger"
                  aria-label="Remove condition"
                  onclick={() => removeCondition(i)}
                >
                  &#10005;
                </button>
              </li>
            {/each}
          </ul>
        {/if}
      </div>

      <!-- Actions -->
      <div class="subsection">
        <div class="subsection-header">
          <span class="subsection-title">Actions</span>
          <button type="button" class="small-btn" onclick={addAction}>
            Add action
          </button>
        </div>

        {#if editActions.length === 0}
          <p class="muted">No actions — filter matches but does nothing.</p>
        {:else}
          <ul class="action-list">
            {#each editActions as action, i (i)}
              <li class="action-row">
                <select
                  value={action.kind}
                  onchange={(e) =>
                    setActionKind(i, (e.currentTarget as HTMLSelectElement).value as ActionKind)}
                  aria-label="Action kind"
                >
                  {#each ACTION_KINDS as k (k.value)}
                    <option value={k.value}>{k.label}</option>
                  {/each}
                </select>

                {#if action.kind === 'apply-label'}
                  <input
                    type="text"
                    value={String(action.params?.label ?? '')}
                    oninput={(e) =>
                      setActionParam(i, 'label', (e.currentTarget as HTMLInputElement).value)}
                    placeholder="Label name"
                    aria-label="Label name"
                  />
                {:else if action.kind === 'forward'}
                  <input
                    type="email"
                    value={String(action.params?.to ?? '')}
                    oninput={(e) =>
                      setActionParam(i, 'to', (e.currentTarget as HTMLInputElement).value)}
                    placeholder="forward@example.com"
                    aria-label="Forward address"
                  />
                {/if}

                <button
                  type="button"
                  class="icon-btn danger"
                  aria-label="Remove action"
                  onclick={() => removeAction(i)}
                >
                  &#10005;
                </button>
              </li>
            {/each}
          </ul>
        {/if}
      </div>

      <!-- Validation error -->
      {#if validationError}
        <p class="validation-error" role="alert">{validationError}</p>
      {/if}

      <!-- Test against existing mail -->
      <div class="test-row">
        <button
          type="button"
          class="small-btn"
          onclick={() => void runTest()}
          disabled={testRunning || editConditions.length === 0}
        >
          {testRunning ? 'Testing…' : 'Test against existing mail'}
        </button>
        {#if testCount !== null}
          <span class="test-result">
            Matches {testCount} thread{testCount === 1 ? '' : 's'} in your mailbox.
          </span>
        {/if}
      </div>

      <!-- Save / cancel -->
      <div class="action-row">
        <button
          type="button"
          class="primary"
          onclick={() => void saveRule()}
          disabled={saving}
        >
          {saving ? 'Saving…' : editorMode === 'create' ? 'Create filter' : 'Save filter'}
        </button>
        <button type="button" onclick={cancelEditor}>
          Cancel
        </button>
      </div>
    </section>
  {/if}

  <!-- ── Blocked senders ────────────────────────────────────────────────── -->

  <section class="form-section">
    <h3>Blocked senders</h3>
    <p class="hint">
      Messages from blocked senders are moved to Trash on arrival. To block a
      sender, use the "Block sender" option in the per-message menu.
    </p>

    {#if blockedSenderRules.length === 0}
      <p class="muted">No senders blocked.</p>
    {:else}
      <ul class="blocked-list">
        {#each blockedSenderRules as rule (rule.id)}
          {@const addr = blockedSenderAddress(rule)}
          <li class="blocked-row">
            <span class="blocked-addr">{addr ?? rule.name}</span>
            <button
              type="button"
              class="small-btn"
              onclick={() => void managedRules.unblockSender(addr ?? '')}
            >
              Unblock
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  </section>
{/if}

<style>
  .form-section {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-03);
    margin-bottom: var(--spacing-06);
  }

  .section-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: var(--spacing-04);
  }

  h3 {
    font-size: var(--type-heading-compact-02-size);
    line-height: var(--type-heading-compact-02-line);
    font-weight: var(--type-heading-compact-02-weight);
    margin: 0;
    color: var(--text-secondary);
  }

  .hint {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }
  .error {
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }
  .muted {
    color: var(--text-helper);
    font-style: italic;
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  /* ── Rules list ────────────────────────────────────────────────────────── */

  .rule-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .rule-row {
    display: flex;
    align-items: flex-start;
    gap: var(--spacing-03);
    padding: var(--spacing-03) var(--spacing-03);
    background: var(--layer-01);
    border-radius: var(--radius-md);
    min-height: var(--touch-min);
    flex-wrap: wrap;
  }
  .rule-row.disabled {
    opacity: 0.6;
  }

  .rule-order-btns {
    display: flex;
    flex-direction: column;
    gap: 2px;
    flex-shrink: 0;
    padding-top: 2px;
  }

  .rule-summary {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
    min-width: 0;
  }
  .rule-name {
    font-weight: 600;
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .rule-detail {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .rule-actions {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    flex-shrink: 0;
  }

  .confirm-delete {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }

  /* ── Editor ────────────────────────────────────────────────────────────── */

  .editor {
    background: var(--layer-01);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-04);
  }

  .field-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    flex-wrap: wrap;
  }

  .field-row label {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-01);
    flex: 1;
    min-width: 200px;
  }
  .field-label {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
  }

  .inline-check {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
    flex-direction: row !important;
    flex: 0 0 auto !important;
  }
  .inline-check input[type='checkbox'] {
    width: 16px;
    height: 16px;
    accent-color: var(--interactive);
    cursor: pointer;
  }

  .subsection {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }
  .subsection-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  .subsection-title {
    font-size: var(--type-body-compact-01-size);
    font-weight: 600;
    color: var(--text-primary);
  }

  .cond-list,
  .action-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }

  .cond-row,
  .action-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-02);
    flex-wrap: wrap;
  }

  .cond-bool {
    color: var(--text-helper);
    font-size: var(--type-body-compact-01-size);
    font-style: italic;
  }

  .validation-error {
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--layer-02);
    border: 1px solid var(--support-error);
    border-radius: var(--radius-md);
  }

  .test-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    flex-wrap: wrap;
  }
  .test-result {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-primary);
  }

  .action-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    flex-wrap: wrap;
  }

  /* ── Blocked senders ───────────────────────────────────────────────────── */

  .blocked-list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }
  .blocked-row {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    padding: var(--spacing-02) var(--spacing-03);
    background: var(--layer-01);
    border-radius: var(--radius-md);
    min-height: var(--touch-min);
  }
  .blocked-addr {
    flex: 1;
    font-family: var(--font-mono);
    font-size: var(--type-code-01-size);
    color: var(--text-primary);
    word-break: break-all;
  }

  /* ── Shared input styles ─────────────────────────────────────────────── */

  input[type='text'],
  input[type='email'],
  select {
    background: var(--layer-01);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-02) var(--spacing-03);
    min-height: var(--touch-min);
    font-family: inherit;
    font-size: var(--type-body-compact-01-size);
  }
  input[type='text']:focus,
  input[type='email']:focus,
  select:focus {
    border-color: var(--interactive);
    outline: none;
  }
  input[type='text'] {
    flex: 1;
  }

  /* ── Toggle switch ───────────────────────────────────────────────────── */

  .switch {
    position: relative;
    display: inline-flex;
    width: 36px;
    height: 20px;
    cursor: pointer;
    flex-shrink: 0;
  }
  .switch input {
    position: absolute;
    inset: 0;
    opacity: 0;
    width: 100%;
    height: 100%;
    margin: 0;
    cursor: pointer;
  }
  .switch .track {
    width: 100%;
    height: 100%;
    background: var(--layer-02);
    border-radius: var(--radius-pill);
    position: relative;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  .switch .track::before {
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
  .switch input:checked + .track {
    background: var(--interactive);
  }
  .switch input:checked + .track::before {
    transform: translateX(16px);
  }

  /* ── Buttons ─────────────────────────────────────────────────────────── */

  .icon-btn {
    width: 28px;
    height: 28px;
    border-radius: var(--radius-sm);
    color: var(--text-secondary);
    font-size: 12px;
    display: flex;
    align-items: center;
    justify-content: center;
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    flex-shrink: 0;
  }
  .icon-btn:hover:not(:disabled) {
    background: var(--layer-02);
    color: var(--text-primary);
  }
  .icon-btn:disabled {
    opacity: 0.35;
    cursor: not-allowed;
  }
  .icon-btn.danger:hover:not(:disabled) {
    background: var(--support-error);
    color: var(--text-on-color);
  }

  .small-btn {
    padding: var(--spacing-01) var(--spacing-03);
    border-radius: var(--radius-md);
    background: var(--layer-02);
    color: var(--text-primary);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
    white-space: nowrap;
  }
  .small-btn:hover:not(:disabled) {
    background: var(--layer-03);
  }
  .small-btn:disabled {
    opacity: 0.4;
    cursor: not-allowed;
  }
  .small-btn.danger:hover:not(:disabled) {
    background: var(--support-error);
    color: var(--text-on-color);
  }

  .primary {
    padding: var(--spacing-03) var(--spacing-05);
    background: var(--interactive);
    color: var(--text-on-color);
    border-radius: var(--radius-pill);
    font-weight: 600;
    min-height: var(--touch-min);
  }
  .primary:hover:not(:disabled) {
    filter: brightness(1.1);
  }
  .primary:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  button:not(.icon-btn):not(.small-btn):not(.primary) {
    padding: var(--spacing-02) var(--spacing-04);
    border-radius: var(--radius-md);
    background: var(--layer-02);
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    min-height: var(--touch-min);
    transition: background var(--duration-fast-02) var(--easing-productive-enter);
  }
  button:not(.icon-btn):not(.small-btn):not(.primary):hover {
    background: var(--layer-03);
    color: var(--text-primary);
  }
</style>
