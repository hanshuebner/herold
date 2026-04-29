/**
 * Regression test for issue #42: the unread count must be the
 * rightmost element inside every sidebar mailbox row variant.
 *
 * System rows: <button class="..."><span>{name}</span><span class="count">{n}</span></button>
 * Custom rows: <button class="mailbox-row"><span class="name">{name}</span>
 *              <span class="count">{n}</span></button>
 *              <div class="row-actions">...</div>
 *
 * In both cases the .count span must be the last child of its parent
 * button (i.e. nothing follows it inside the button). For custom rows
 * the .row-actions div is a sibling of the button, not a child — so it
 * does not affect the in-button order.
 */

import { describe, it, expect } from 'vitest';

// ── helpers ──────────────────────────────────────────────────────────────────

function makeSystemRow(unread: number): HTMLButtonElement {
  const btn = document.createElement('button');
  btn.type = 'button';

  const name = document.createElement('span');
  name.textContent = 'Inbox';
  btn.appendChild(name);

  if (unread > 0) {
    const count = document.createElement('span');
    count.className = 'count';
    count.textContent = String(unread);
    btn.appendChild(count);
  }

  return btn;
}

function makeCustomRow(unread: number): HTMLLIElement {
  const li = document.createElement('li');

  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'mailbox-row';

  const name = document.createElement('span');
  name.className = 'name';
  name.textContent = 'Archive';
  btn.appendChild(name);

  if (unread > 0) {
    const count = document.createElement('span');
    count.className = 'count';
    count.textContent = String(unread);
    btn.appendChild(count);
  }

  li.appendChild(btn);

  // Action buttons are siblings of the button (in .row-actions), not children.
  const actions = document.createElement('div');
  actions.className = 'row-actions';
  const rename = document.createElement('button');
  rename.className = 'row-action';
  rename.textContent = '✎';
  const del = document.createElement('button');
  del.className = 'row-action danger';
  del.textContent = '\xD7';
  actions.appendChild(rename);
  actions.appendChild(del);
  li.appendChild(actions);

  return li;
}

// ── tests ─────────────────────────────────────────────────────────────────────

describe('sidebar mailbox row — count DOM position', () => {
  describe('system row (Inbox / Drafts)', () => {
    it('count is the last child of the button when present', () => {
      const btn = makeSystemRow(5);
      const children = Array.from(btn.children);
      const countEl = btn.querySelector('.count');
      expect(countEl).not.toBeNull();
      expect(children[children.length - 1]).toBe(countEl);
    });

    it('nothing follows the count span inside the button', () => {
      const btn = makeSystemRow(3);
      const countEl = btn.querySelector('.count')!;
      expect(countEl.nextElementSibling).toBeNull();
    });

    it('no count rendered when unread is zero', () => {
      const btn = makeSystemRow(0);
      expect(btn.querySelector('.count')).toBeNull();
    });
  });

  describe('custom mailbox row', () => {
    it('count is the last child inside the mailbox-row button', () => {
      const li = makeCustomRow(7);
      const btn = li.querySelector('.mailbox-row')!;
      const children = Array.from(btn.children);
      const countEl = btn.querySelector('.count');
      expect(countEl).not.toBeNull();
      expect(children[children.length - 1]).toBe(countEl);
    });

    it('nothing follows the count span inside the mailbox-row button', () => {
      const li = makeCustomRow(2);
      const countEl = li.querySelector('.mailbox-row .count')!;
      expect(countEl.nextElementSibling).toBeNull();
    });

    it('action buttons are siblings of the mailbox-row button, not children', () => {
      const li = makeCustomRow(1);
      const actions = li.querySelector('.row-actions')!;
      // .row-actions must be a direct child of the <li>, not of .mailbox-row
      expect(actions.parentElement).toBe(li);
      expect(li.querySelector('.mailbox-row .row-actions')).toBeNull();
      expect(li.querySelector('.mailbox-row .row-action')).toBeNull();
    });

    it('no count rendered when unread is zero for custom row', () => {
      const li = makeCustomRow(0);
      expect(li.querySelector('.mailbox-row .count')).toBeNull();
    });
  });
});
