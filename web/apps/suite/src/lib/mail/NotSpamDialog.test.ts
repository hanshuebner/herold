/**
 * Component tests for NotSpamDialog (issue #382 retry items 2 and 3).
 *
 * Item 2 (undo must not orphan the rule): mail.notSpam() returns its
 * move-undo without showing a toast; the dialog shows one combined
 * toast after the rule step settles, so clicking the toast's Undo
 * reverts the move AND cleans up whatever the rule step did (deletes a
 * created rule, or reverts an added action). A rule that was merely
 * reused (already had the never-spam action) is left untouched.
 *
 * Item 3 (dedup existing rules): choosing "This domain" on two Junk
 * messages from the same domain must produce exactly one ManagedRule,
 * not two -- the second confirm reuses the first rule instead of
 * creating a duplicate.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import NotSpamDialog from './NotSpamDialog.svelte';
import type { ManagedRule } from '../settings/managed-rules.svelte';

const { mailMock, managedRulesMock } = vi.hoisted(() => {
  const mailMock = {
    notSpam: vi.fn(),
  };

  let idCounter = 0;
  const managedRulesMock: {
    rules: ManagedRule[];
    create: ReturnType<typeof vi.fn>;
    update: ReturnType<typeof vi.fn>;
    delete: ReturnType<typeof vi.fn>;
  } = {
    rules: [],
    create: vi.fn(async (payload: Omit<ManagedRule, 'id'>) => {
      idCounter += 1;
      const rule: ManagedRule = { id: `r${idCounter}`, ...payload };
      managedRulesMock.rules = [...managedRulesMock.rules, rule];
      return rule;
    }),
    update: vi.fn(async (id: string, patches: Partial<Omit<ManagedRule, 'id'>>) => {
      managedRulesMock.rules = managedRulesMock.rules.map((r) =>
        r.id === id ? { ...r, ...patches } : r,
      );
      return true;
    }),
    delete: vi.fn(async (id: string) => {
      managedRulesMock.rules = managedRulesMock.rules.filter((r) => r.id !== id);
      return true;
    }),
  };

  return { mailMock, managedRulesMock };
});

vi.mock('./store.svelte', () => ({ mail: mailMock }));
vi.mock('../settings/managed-rules.svelte', async () => {
  const actual = await vi.importActual<typeof import('../settings/managed-rules.svelte')>(
    '../settings/managed-rules.svelte',
  );
  return { ...actual, managedRules: managedRulesMock };
});
vi.mock('../i18n/i18n.svelte', () => ({
  t: (key: string) => key,
  localeTag: () => 'en',
}));

// The real toast singleton: NotSpamDialog's undo wiring is only
// meaningful end to end (calling toast.undo() must invoke the
// dialog-built undo closure), so it is not mocked here.
import { toast } from '../toast/toast.svelte';

function renderDialog(emailId: string, senderEmail: string) {
  const onclose = vi.fn();
  const onmoved = vi.fn();
  const utils = render(NotSpamDialog, { props: { emailId, senderEmail, onclose, onmoved } });
  return { ...utils, onclose, onmoved };
}

beforeEach(() => {
  mailMock.notSpam.mockReset();
  managedRulesMock.rules = [];
  managedRulesMock.create.mockClear();
  managedRulesMock.update.mockClear();
  managedRulesMock.delete.mockClear();
  toast.dismiss();
});

describe('NotSpamDialog undo consistency (issue #382 retry item 2)', () => {
  it('undo after creating a never-spam rule deletes the rule and reverts the move', async () => {
    const moveUndo = vi.fn().mockResolvedValue(undefined);
    mailMock.notSpam.mockResolvedValue({ ok: true, undo: moveUndo });

    renderDialog('e-1', 'notify@accountprotection.microsoft.com');

    await fireEvent.click(screen.getByTestId('not-spam-dialog-scope-domain'));
    await fireEvent.click(screen.getByRole('button', { name: 'mail.notSpam.confirm' }));

    await waitFor(() => {
      expect(managedRulesMock.create).toHaveBeenCalledTimes(1);
    });
    expect(managedRulesMock.rules).toHaveLength(1);
    const createdId = managedRulesMock.rules[0]!.id;

    expect(toast.current).not.toBeNull();
    await toast.undo();

    expect(moveUndo).toHaveBeenCalledTimes(1);
    expect(managedRulesMock.delete).toHaveBeenCalledWith(createdId);
    expect(managedRulesMock.rules).toHaveLength(0);
  });

  it('undo after reusing an existing never-spam rule reverts only the move (no delete)', async () => {
    const existing: ManagedRule = {
      id: 'existing-1',
      name: 'Allow example.test',
      enabled: true,
      order: 0,
      conditions: [{ field: 'from-domain', op: 'equals', value: 'example.test' }],
      actions: [{ kind: 'never-spam' }],
    };
    managedRulesMock.rules = [existing];

    const moveUndo = vi.fn().mockResolvedValue(undefined);
    mailMock.notSpam.mockResolvedValue({ ok: true, undo: moveUndo });

    renderDialog('e-2', 'someone@example.test');

    await fireEvent.click(screen.getByTestId('not-spam-dialog-scope-domain'));
    await fireEvent.click(screen.getByRole('button', { name: 'mail.notSpam.confirm' }));

    await waitFor(() => {
      expect(mailMock.notSpam).toHaveBeenCalledWith('e-2');
    });
    expect(managedRulesMock.create).not.toHaveBeenCalled();

    await toast.undo();

    expect(moveUndo).toHaveBeenCalledTimes(1);
    expect(managedRulesMock.delete).not.toHaveBeenCalled();
    expect(managedRulesMock.update).not.toHaveBeenCalled();
    // The pre-existing rule is untouched by the undo.
    expect(managedRulesMock.rules).toEqual([existing]);
  });
});

describe('NotSpamDialog rule dedup (issue #382 retry item 3)', () => {
  it('choosing "This domain" on two Junk messages from one domain yields exactly one rule', async () => {
    mailMock.notSpam.mockResolvedValue({ ok: true, undo: vi.fn() });

    const first = renderDialog('e-3', 'notify@accountprotection.microsoft.com');
    await fireEvent.click(screen.getByTestId('not-spam-dialog-scope-domain'));
    await fireEvent.click(screen.getByRole('button', { name: 'mail.notSpam.confirm' }));
    await waitFor(() => {
      expect(managedRulesMock.create).toHaveBeenCalledTimes(1);
    });
    expect(managedRulesMock.rules).toHaveLength(1);
    first.unmount();
    cleanup();

    const second = renderDialog('e-4', 'billing@accountprotection.microsoft.com');
    await fireEvent.click(screen.getByTestId('not-spam-dialog-scope-domain'));
    await fireEvent.click(screen.getByRole('button', { name: 'mail.notSpam.confirm' }));
    await waitFor(() => {
      expect(mailMock.notSpam).toHaveBeenCalledWith('e-4');
    });

    // No second create call: the existing rule for the domain is reused.
    expect(managedRulesMock.create).toHaveBeenCalledTimes(1);
    expect(managedRulesMock.rules).toHaveLength(1);
    second.unmount();
  });
});
