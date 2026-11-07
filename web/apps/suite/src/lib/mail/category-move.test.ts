import { describe, it, expect, vi, beforeEach } from 'vitest';

const { mailMock, toastShow, rules, addRule } = vi.hoisted(() => ({
  mailMock: {
    emails: new Map<string, { from: { email: string }[] | null }>(),
    setCategoryKeyword: vi.fn(async () => true),
  },
  toastShow: vi.fn(),
  rules: { list: [] as { sender: string; category: string }[] },
  addRule: vi.fn(async () => true),
}));

vi.mock('./store.svelte', () => ({ mail: mailMock }));
vi.mock('../toast/toast.svelte', () => ({ toast: { show: toastShow } }));
vi.mock('../settings/category-settings.svelte', () => ({
  categorySettings: {
    available: true,
    get senderRules() {
      return rules.list;
    },
    addSenderRule: addRule,
  },
  categoryKeyword: (n: string) => `$category-${n.toLowerCase()}`,
  categoryLabel: (n: string) => n.charAt(0).toUpperCase() + n.slice(1),
}));

import { moveEmailsToCategory, singleSender } from './category-move';

beforeEach(() => {
  vi.clearAllMocks();
  rules.list = [];
  mailMock.setCategoryKeyword.mockResolvedValue(true);
  mailMock.emails = new Map([
    ['a', { from: [{ email: 'News@Shop.example' }] }],
    ['b', { from: [{ email: 'news@shop.example' }] }],
    ['c', { from: [{ email: 'other@x.example' }] }],
  ]);
});

describe('singleSender', () => {
  it('folds case and accepts one distinct sender', () => {
    expect(singleSender(['a', 'b'])).toBe('news@shop.example');
  });
  it('returns null for several senders', () => {
    expect(singleSender(['a', 'c'])).toBeNull();
  });
});

describe('moveEmailsToCategory', () => {
  it('moves every dragged id and offers the rule for one sender', async () => {
    expect(await moveEmailsToCategory(['a', 'b'], 'promotions')).toBe(true);
    expect(mailMock.setCategoryKeyword).toHaveBeenCalledTimes(2);
    expect(toastShow).toHaveBeenCalledTimes(1);
    const spec = toastShow.mock.calls[0]![0];
    await spec.undo();
    expect(addRule).toHaveBeenCalledWith('news@shop.example', 'promotions');
  });

  it('offers no rule for several senders', async () => {
    await moveEmailsToCategory(['a', 'c'], 'promotions');
    expect(toastShow).not.toHaveBeenCalled();
  });

  it('offers no rule when the same rule exists', async () => {
    rules.list = [{ sender: 'news@shop.example', category: 'promotions' }];
    await moveEmailsToCategory(['a'], 'promotions');
    expect(toastShow).not.toHaveBeenCalled();
  });

  it('offers no rule when a move fails', async () => {
    mailMock.setCategoryKeyword.mockResolvedValue(false);
    expect(await moveEmailsToCategory(['a'], 'promotions')).toBe(false);
    expect(toastShow).not.toHaveBeenCalled();
  });
});
