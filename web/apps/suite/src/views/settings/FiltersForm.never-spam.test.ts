/**
 * FiltersForm never-spam action (REQ-FLT-16, issue #382).
 *
 * The structured filter editor lists "never classify as spam" among the
 * action kinds a user filter can carry, so a filter created any other way
 * (including the "Not spam" affordance's own ManagedRule/set call) can be
 * edited from Settings -> Filters, and the rules list renders it in the
 * human-readable actions summary.
 *
 * i18n is NOT mocked (matches store.setError.test.ts / the delivery-override
 * modal test) -- the rule-list summary interpolates {actions} into a real
 * sentence, and a passthrough mock would hide that behind an un-interpolated
 * key.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent, screen, within } from '@testing-library/svelte';
import { i18n, t } from '../../lib/i18n/i18n.svelte';
import type { ManagedRule } from '../../lib/settings/managed-rules.svelte';

const existingRule: ManagedRule = {
  id: 'r1',
  name: 'Allow accountprotection.microsoft.com',
  enabled: true,
  order: 0,
  conditions: [{ field: 'from-domain', op: 'equals', value: 'accountprotection.microsoft.com' }],
  actions: [{ kind: 'never-spam' }],
};

const { managedRulesMock } = vi.hoisted(() => ({
  managedRulesMock: {
    loadStatus: 'ready' as const,
    loadError: null as string | null,
    rules: [] as ManagedRule[],
    load: vi.fn(async () => {}),
    create: vi.fn(
      async (rule: Omit<ManagedRule, 'id'>) => ({ ...rule, id: 'new1' }) as ManagedRule,
    ),
    update: vi.fn(async () => true),
    delete: vi.fn(async () => true),
    setEnabled: vi.fn(async () => {}),
    setOrder: vi.fn(async () => {}),
    testFilter: vi.fn(async () => 0),
    unblockSender: vi.fn(async () => true),
  },
}));

vi.mock('../../lib/settings/managed-rules.svelte', async () => {
  const actual = await vi.importActual<typeof import('../../lib/settings/managed-rules.svelte')>(
    '../../lib/settings/managed-rules.svelte',
  );
  return {
    ...actual,
    managedRules: managedRulesMock,
  };
});

vi.mock('../../lib/toast/toast.svelte', () => ({
  toast: { show: vi.fn(), dismiss: vi.fn(), current: null },
}));

import FiltersForm from './FiltersForm.svelte';

beforeEach(() => {
  vi.clearAllMocks();
  i18n.locale = 'en';
  managedRulesMock.loadStatus = 'ready';
  managedRulesMock.rules = [];
});

describe('FiltersForm never-spam action (issue #382)', () => {
  it('lists "never classify as spam" in the action-kind dropdown', () => {
    render(FiltersForm);

    fireEvent.click(screen.getByRole('button', { name: t('settings.filters.create') }));

    const actionSelect = screen.getByLabelText(t('settings.filters.actionKind'));
    const optionLabels = Array.from(actionSelect.querySelectorAll('option')).map(
      (o) => o.textContent,
    );
    expect(optionLabels).toContain(t('settings.filters.action.neverSpam'));
  });

  it('saves a rule created with the never-spam action', async () => {
    render(FiltersForm);

    // The "New filter" header button and, once the editor opens, the
    // create-mode Save button share the label "Create filter" -- open via
    // the first match, submit via the last (the editor's Save button).
    await fireEvent.click(screen.getAllByRole('button', { name: t('settings.filters.create') })[0]!);

    const conditionValue = screen.getByLabelText(t('settings.filters.conditionValue'));
    await fireEvent.input(conditionValue, { target: { value: 'accountprotection.microsoft.com' } });

    const actionSelect = screen.getByLabelText(t('settings.filters.actionKind')) as HTMLSelectElement;
    await fireEvent.change(actionSelect, { target: { value: 'never-spam' } });

    const saveButtons = screen.getAllByRole('button', { name: t('settings.filters.create') });
    await fireEvent.click(saveButtons[saveButtons.length - 1]!);

    expect(managedRulesMock.create).toHaveBeenCalledWith(
      expect.objectContaining({
        actions: [{ kind: 'never-spam' }],
      }),
    );
  });

  it('renders never-spam in the rule-list actions summary', () => {
    managedRulesMock.rules = [existingRule];
    render(FiltersForm);

    const summary = screen.getByText(existingRule.name).closest<HTMLElement>('.rule-summary')!;
    expect(
      within(summary).getByText(t('settings.filters.action.neverSpam'), { exact: false }),
    ).toBeInTheDocument();
  });
});
