<script lang="ts">
  /**
   * Vacation auto-responder form per RFC 8621 §8 (VacationResponse).
   * The server enforces the singleton — one row per account, id =
   * "singleton". This form does VacationResponse/get on mount and
   * VacationResponse/set { update } on save.
   */
  import { jmap, strict } from '../../lib/jmap/client';
  import { Capability } from '../../lib/jmap/types';
  import { mail } from '../../lib/mail/store.svelte';
  import { toast } from '../../lib/toast/toast.svelte';
  import { t } from '../../lib/i18n/i18n.svelte';

  interface VacationResponse {
    id: string;
    isEnabled: boolean;
    fromDate: string | null;
    toDate: string | null;
    subject: string | null;
    textBody: string | null;
    htmlBody: string | null;
  }

  type LoadStatus = 'idle' | 'loading' | 'ready' | 'error';

  let status = $state<LoadStatus>('idle');
  let error = $state<string | null>(null);
  let saving = $state(false);

  // Form fields, edited locally; saving copies them onto a synthetic
  // VacationResponse and dispatches /set.
  let isEnabled = $state(false);
  let fromDate = $state(''); // ISO yyyy-mm-ddThh:mm
  let toDate = $state('');
  let subject = $state('');
  let textBody = $state('');

  $effect(() => {
    if (status === 'idle') void load();
  });

  async function load(): Promise<void> {
    const accountId = mail.mailAccountId;
    if (!accountId) {
      status = 'error';
      error = t('settings.vacation.noAccount');
      return;
    }
    status = 'loading';
    error = null;
    try {
      const { responses } = await jmap.batch((b) => {
        b.call(
          'VacationResponse/get',
          { accountId, ids: ['singleton'] },
          [Capability.VacationResponse],
        );
      });
      strict(responses);
      const inv = responses[0]!;
      const args = inv[1] as { list: VacationResponse[] };
      const v = args.list?.[0] ?? null;
      isEnabled = v?.isEnabled ?? false;
      fromDate = isoToLocalDateTimeInput(v?.fromDate ?? null);
      toDate = isoToLocalDateTimeInput(v?.toDate ?? null);
      subject = v?.subject ?? '';
      textBody = v?.textBody ?? '';
      status = 'ready';
    } catch (err) {
      status = 'error';
      error = err instanceof Error ? err.message : t('settings.vacation.loadFailed');
    }
  }

  async function save(): Promise<void> {
    const accountId = mail.mailAccountId;
    if (!accountId) return;
    saving = true;
    try {
      const update: Record<string, unknown> = {
        isEnabled,
        fromDate: localDateTimeInputToIso(fromDate),
        toDate: localDateTimeInputToIso(toDate),
        subject: subject.trim() || null,
        textBody: textBody.trim() || null,
      };
      const { responses } = await jmap.batch((b) => {
        b.call(
          'VacationResponse/set',
          {
            accountId,
            update: { singleton: update },
          },
          [Capability.VacationResponse],
        );
      });
      strict(responses);
      const inv = responses[0]!;
      const result = inv[1] as {
        notUpdated?: Record<string, { type: string; description?: string }>;
      };
      const fail = result.notUpdated?.singleton;
      if (fail) {
        toast.show({
          message: fail.description ?? t('settings.vacation.saveFailedReason', { reason: fail.type }),
          kind: 'error',
          timeoutMs: 6000,
        });
        return;
      }
      toast.show({
        message: isEnabled
          ? t('settings.vacation.enabled')
          : t('settings.vacation.disabled'),
      });
    } catch (err) {
      toast.show({
        message: err instanceof Error ? err.message : t('settings.vacation.saveFailed'),
        kind: 'error',
        timeoutMs: 6000,
      });
    } finally {
      saving = false;
    }
  }

  /** RFC 3339 UTC → "yyyy-mm-ddThh:mm" in the user's local zone. */
  function isoToLocalDateTimeInput(iso: string | null): string {
    if (!iso) return '';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return '';
    const pad = (n: number): string => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }

  /** "yyyy-mm-ddThh:mm" (local) → RFC 3339 UTC, or null when empty. */
  function localDateTimeInputToIso(local: string): string | null {
    if (!local.trim()) return null;
    const d = new Date(local);
    if (Number.isNaN(d.getTime())) return null;
    return d.toISOString();
  }
</script>

{#if status === 'loading' || status === 'idle'}
  <p class="hint">{t('common.loading')}</p>
{:else if status === 'error'}
  <p class="error" role="alert">{error}</p>
  <button type="button" onclick={() => void load()}>{t('common.retry')}</button>
{:else}
  <div class="row">
    <span class="label">{t('settings.vacation.autoReply')}</span>
    <label class="switch">
      <input type="checkbox" bind:checked={isEnabled} />
      <span class="track" aria-hidden="true"></span>
    </label>
  </div>

  <div class="row vertical">
    <span class="label">{t('settings.vacation.activeFrom')}</span>
    <input
      type="datetime-local"
      bind:value={fromDate}
      disabled={!isEnabled}
    />
    <p class="hint">{t('settings.vacation.activeFromHint')}</p>
  </div>

  <div class="row vertical">
    <span class="label">{t('settings.vacation.activeUntil')}</span>
    <input
      type="datetime-local"
      bind:value={toDate}
      disabled={!isEnabled}
    />
    <p class="hint">{t('settings.vacation.activeUntilHint')}</p>
  </div>

  <div class="row vertical">
    <span class="label">{t('settings.vacation.subject')}</span>
    <input
      type="text"
      placeholder={t('settings.vacation.subjectPlaceholder')}
      bind:value={subject}
      disabled={!isEnabled}
    />
  </div>

  <div class="row vertical">
    <span class="label">{t('settings.vacation.body')}</span>
    <textarea rows="5" bind:value={textBody} disabled={!isEnabled}></textarea>
  </div>

  <div class="row">
    <span class="label"></span>
    <button type="button" class="primary" onclick={() => void save()} disabled={saving}>
      {saving ? t('common.saving') : t('common.save')}
    </button>
  </div>
{/if}

<style>
  .row {
    display: flex;
    align-items: center;
    gap: var(--spacing-04);
    padding: var(--spacing-03) 0;
    border-bottom: 1px solid var(--border-subtle-01);
  }
  .row.vertical {
    flex-direction: column;
    align-items: stretch;
    gap: var(--spacing-02);
  }
  .label {
    color: var(--text-secondary);
    font-size: var(--type-body-compact-01-size);
    flex: 0 0 auto;
    min-width: 12em;
  }
  .row.vertical .label {
    min-width: 0;
  }

  input[type='text'],
  input[type='datetime-local'],
  textarea {
    background: var(--layer-01);
    color: var(--text-primary);
    border: 1px solid var(--border-subtle-01);
    border-radius: var(--radius-md);
    padding: var(--spacing-02) var(--spacing-03);
    min-height: var(--touch-min);
    font-family: inherit;
    font-size: var(--type-body-01-size);
  }
  textarea {
    min-height: 120px;
    resize: vertical;
  }
  input:disabled,
  textarea:disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }

  .switch {
    position: relative;
    display: inline-flex;
    width: 44px;
    height: 24px;
    cursor: pointer;
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
    width: 20px;
    height: 20px;
    background: var(--text-on-color);
    border-radius: var(--radius-pill);
    transition: transform var(--duration-fast-02) var(--easing-productive-enter);
  }
  .switch input:checked + .track {
    background: var(--interactive);
  }
  .switch input:checked + .track::before {
    transform: translateX(20px);
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
    cursor: progress;
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
</style>
