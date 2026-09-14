<script lang="ts">
  /**
   * "Not spam" dialog (issue #382, REQ-FLT-16 / REQ-FILT-02a).
   *
   * Triggered from ThreadToolbar's "Not spam" action on a Junk message.
   * Moves the message to Inbox (mail.notSpam, which posts the
   * REQ-FILT-70 ham feedback record itself) and, when the user opts in
   * via the scope chooser, creates -- or reuses / extends -- a
   * never-spam ManagedRule scoped to the sender's exact address or its
   * domain so future mail from that sender skips Junk filing regardless
   * of the classifier verdict (planNeverSpamRule dedups against the
   * caller's existing rules, issue #382 retry).
   *
   * mail.notSpam() returns its move-undo without showing a toast; this
   * dialog shows the toast only after the rule step has also settled, so
   * a single toast/undo covers both the move and the rule change in one
   * step and clicking Undo can never leave an orphaned rule behind.
   */
  import { mail } from './store.svelte';
  import { managedRules } from '../settings/managed-rules.svelte';
  import { toast } from '../toast/toast.svelte';
  import { t } from '../i18n/i18n.svelte';
  import Button from '@herold/design-system/Button.svelte';
  import { senderDomain, planNeverSpamRule, type NeverSpamScope } from './not-spam';

  interface Props {
    emailId: string;
    senderEmail: string;
    onclose: () => void;
    /** Called once the move has actually been kicked off successfully. */
    onmoved: () => void;
  }

  let { emailId, senderEmail, onclose, onmoved }: Props = $props();

  type Scope = NeverSpamScope | 'none';

  let domain = $derived(senderDomain(senderEmail));
  let scope = $state<Scope>('none');
  let moving = $state(false);
  let error = $state<string | null>(null);

  function close(): void {
    if (moving) return;
    onclose();
  }

  function onBackdropClick(e: MouseEvent): void {
    if (e.target === e.currentTarget) close();
  }

  function onKeyDown(e: KeyboardEvent): void {
    if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close();
    }
  }

  async function confirm(): Promise<void> {
    if (moving) return;
    error = null;
    moving = true;
    try {
      const result = await mail.notSpam(emailId);
      if (!result.ok) {
        error = t('mail.notSpam.error');
        return;
      }

      let toastMessage = 'Moved to Inbox';
      // Undoes whatever the rule step below actually did (created a
      // rule, added the never-spam action to an existing one, or
      // nothing at all when an existing rule was reused as-is) so the
      // toast's Undo, which always reverts the move, never leaves an
      // orphaned rule/action behind.
      let undoRuleChange: (() => Promise<void>) | null = null;

      if (scope === 'address' || scope === 'domain') {
        const maxOrder = managedRules.rules.reduce((m, r) => Math.max(m, r.order), -1);
        const ruleName =
          scope === 'address'
            ? t('mail.notSpam.ruleNameAddress', { address: senderEmail })
            : t('mail.notSpam.ruleNameDomain', { domain });
        const plan = planNeverSpamRule(managedRules.rules, scope, senderEmail, maxOrder + 1, ruleName);
        if (plan?.mode === 'create') {
          const created = await managedRules.create(plan.payload);
          if (created) {
            toastMessage = t('mail.notSpam.ruleCreated');
            undoRuleChange = async () => {
              await managedRules.delete(created.id);
            };
          }
          // managedRules.create already toasts its own failure; the move
          // to Inbox still succeeded, so we do not surface a second error.
        } else if (plan?.mode === 'add-action') {
          const rule = plan.rule;
          const previousActions = rule.actions;
          const ok = await managedRules.update(rule.id, { actions: plan.nextActions });
          if (ok) {
            toastMessage = t('mail.notSpam.ruleUpdated', { name: rule.name });
            undoRuleChange = async () => {
              await managedRules.update(rule.id, { actions: previousActions });
            };
          }
        } else if (plan?.mode === 'reuse') {
          // The sender already has a never-spam rule; nothing to
          // create or change, so nothing to undo either.
          toastMessage = t('mail.notSpam.ruleReused', { name: plan.rule.name });
        }
      }

      toast.show({
        message: toastMessage,
        undo: async () => {
          await result.undo();
          if (undoRuleChange) await undoRuleChange();
        },
      });

      onmoved();
      onclose();
    } finally {
      moving = false;
    }
  }
</script>

<svelte:window onkeydown={onKeyDown} />

<div class="backdrop" role="presentation" onclick={onBackdropClick} aria-hidden="true"></div>

<div
  class="dialog"
  role="dialog"
  aria-modal="true"
  aria-labelledby="not-spam-dialog-title"
  tabindex="-1"
  data-testid="not-spam-dialog"
>
  <header class="dialog-header">
    <h3 id="not-spam-dialog-title" class="dialog-title">{t('mail.notSpam.title')}</h3>
    <button
      type="button"
      class="close"
      onclick={close}
      aria-label={t('common.close')}
      data-testid="not-spam-dialog-close"
    >
      &times;
    </button>
  </header>

  <div class="dialog-body">
    <p class="body-text">{t('mail.notSpam.body')}</p>

    <fieldset class="field-label">
      <legend class="label-text">{t('mail.notSpam.scopeHeading')}</legend>
      <div class="radio-group" role="radiogroup">
        <label class="radio">
          <input
            type="radio"
            name="not-spam-scope"
            value="none"
            checked={scope === 'none'}
            onchange={() => (scope = 'none')}
            disabled={moving}
          />
          <span>{t('mail.notSpam.scopeNone')}</span>
        </label>
        <label class="radio">
          <input
            type="radio"
            name="not-spam-scope"
            value="address"
            checked={scope === 'address'}
            onchange={() => (scope = 'address')}
            disabled={moving}
            data-testid="not-spam-dialog-scope-address"
          />
          <span>{t('mail.notSpam.scopeAddress', { address: senderEmail })}</span>
        </label>
        {#if domain}
          <label class="radio">
            <input
              type="radio"
              name="not-spam-scope"
              value="domain"
              checked={scope === 'domain'}
              onchange={() => (scope = 'domain')}
              disabled={moving}
              data-testid="not-spam-dialog-scope-domain"
            />
            <span>{t('mail.notSpam.scopeDomain', { domain })}</span>
          </label>
        {/if}
      </div>
    </fieldset>

    {#if error}
      <p class="error" role="alert">{error}</p>
    {/if}

    <div class="actions">
      <Button variant="secondary" onclick={close} disabled={moving}>
        {t('common.cancel')}
      </Button>
      <Button variant="primary" onclick={() => void confirm()} disabled={moving}>
        {moving ? t('mail.notSpam.moving') : t('mail.notSpam.confirm')}
      </Button>
    </div>
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    z-index: 800;
  }

  .dialog {
    position: fixed;
    top: 50%;
    left: 50%;
    transform: translate(-50%, -50%);
    width: min(480px, calc(100vw - 2 * var(--spacing-05)));
    max-height: calc(100vh - 2 * var(--spacing-07));
    display: flex;
    flex-direction: column;
    background: var(--layer-02);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-lg);
    box-shadow: 0 16px 48px rgba(0, 0, 0, 0.5);
    z-index: 801;
    overflow: hidden;
  }

  .dialog-header {
    display: flex;
    align-items: center;
    padding: var(--spacing-04) var(--spacing-05);
    border-bottom: 1px solid var(--border-subtle-01);
    gap: var(--spacing-04);
  }

  .dialog-title {
    margin: 0;
    flex: 1;
    font-size: var(--type-heading-compact-02-size);
    line-height: var(--type-heading-compact-02-line);
    font-weight: var(--type-heading-compact-02-weight);
    color: var(--text-primary);
  }

  .close {
    color: var(--text-helper);
    font-size: 20px;
    line-height: 1;
    width: 28px;
    height: 28px;
    border-radius: var(--radius-pill);
    flex-shrink: 0;
  }
  .close:hover {
    background: var(--layer-03);
    color: var(--text-primary);
  }

  .dialog-body {
    padding: var(--spacing-05);
    overflow-y: auto;
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: var(--spacing-04);
  }

  .body-text {
    margin: 0;
    color: var(--text-primary);
    font-size: var(--type-body-01-size);
  }

  .field-label {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
    margin: 0;
    border: 0;
    padding: 0;
  }
  .label-text {
    font-size: var(--type-body-compact-01-size);
    color: var(--text-secondary);
    font-weight: 600;
  }

  .radio-group {
    display: flex;
    flex-direction: column;
    gap: var(--spacing-02);
  }
  .radio {
    display: flex;
    align-items: center;
    gap: var(--spacing-03);
    cursor: pointer;
    color: var(--text-primary);
    font-size: var(--type-body-01-size);
  }
  .radio input[type='radio'] {
    accent-color: var(--interactive);
    width: 18px;
    height: 18px;
  }

  .error {
    color: var(--support-error);
    font-size: var(--type-body-compact-01-size);
    margin: 0;
  }

  .actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--spacing-03);
    padding-top: var(--spacing-03);
  }
</style>
