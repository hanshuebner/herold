/**
 * Tests for advanced-search panel helpers.
 *
 * Covers:
 *  - fieldsToQuery: panel fields -> operator:value query string
 *  - queryToFields: query string -> panel fields (round-trip)
 */
import { describe, it, expect } from 'vitest';
import { emptyFields, fieldsToQuery, queryToFields } from './advanced-search';

const noMailboxes = new Map<string, string>();

// Mailbox name->id map for queryToFields.
const mailboxIdByName = new Map<string, string>([
  ['inbox', 'mb-inbox'],
  ['work', 'mb-work'],
]);

// Mailbox id->name map for fieldsToQuery.
const mailboxNameById = new Map<string, string>([
  ['mb-inbox', 'inbox'],
  ['mb-work', 'work'],
]);

describe('fieldsToQuery', () => {
  it('returns empty string for empty fields', () => {
    expect(fieldsToQuery(emptyFields(), noMailboxes)).toBe('');
  });

  it('emits a from: token', () => {
    const f = { ...emptyFields(), from: 'alice@example.com' };
    expect(fieldsToQuery(f, noMailboxes)).toBe('from:alice@example.com');
  });

  it('emits a to: token', () => {
    const f = { ...emptyFields(), to: 'bob@example.com' };
    expect(fieldsToQuery(f, noMailboxes)).toBe('to:bob@example.com');
  });

  it('quotes multi-word values', () => {
    const f = { ...emptyFields(), subject: 'weekly meeting' };
    expect(fieldsToQuery(f, noMailboxes)).toBe('subject:"weekly meeting"');
  });

  it('emits after: and before: tokens', () => {
    const f = { ...emptyFields(), after: '2024-01-01', before: '2024-12-31' };
    const q = fieldsToQuery(f, noMailboxes);
    expect(q).toContain('after:2024-01-01');
    expect(q).toContain('before:2024-12-31');
  });

  it('resolves mailboxId to label: token via the name map', () => {
    const f = { ...emptyFields(), mailboxId: 'mb-work' };
    expect(fieldsToQuery(f, mailboxNameById)).toBe('label:work');
  });

  it('omits a label: token when mailboxId has no matching name', () => {
    const f = { ...emptyFields(), mailboxId: 'mb-unknown' };
    expect(fieldsToQuery(f, mailboxNameById)).toBe('');
  });

  it('emits has:attachment when toggled', () => {
    const f = { ...emptyFields(), hasAttachment: true };
    expect(fieldsToQuery(f, noMailboxes)).toBe('has:attachment');
  });

  it('combines multiple fields in order', () => {
    const f = {
      ...emptyFields(),
      from: 'alice@x.test',
      hasAttachment: true,
    };
    expect(fieldsToQuery(f, noMailboxes)).toBe('from:alice@x.test has:attachment');
  });
});

describe('queryToFields', () => {
  it('returns empty fields for an empty query', () => {
    expect(queryToFields('', mailboxIdByName)).toEqual(emptyFields());
  });

  it('parses from: token', () => {
    const f = queryToFields('from:alice@x.test', mailboxIdByName);
    expect(f.from).toBe('alice@x.test');
  });

  it('parses to: token', () => {
    const f = queryToFields('to:bob@x.test', mailboxIdByName);
    expect(f.to).toBe('bob@x.test');
  });

  it('unquotes multi-word values', () => {
    const f = queryToFields('subject:"weekly meeting"', mailboxIdByName);
    expect(f.subject).toBe('weekly meeting');
  });

  it('parses after: and before: to YYYY-MM-DD', () => {
    const f = queryToFields('after:2024-01-01 before:2024-12-31', mailboxIdByName);
    expect(f.after).toBe('2024-01-01');
    expect(f.before).toBe('2024-12-31');
  });

  it('strips time portion from ISO timestamps for date inputs', () => {
    const f = queryToFields('after:2024-01-01T00:00:00.000Z', mailboxIdByName);
    expect(f.after).toBe('2024-01-01');
  });

  it('resolves label: token to mailboxId via the name map', () => {
    const f = queryToFields('label:work', mailboxIdByName);
    expect(f.mailboxId).toBe('mb-work');
  });

  it('resolves label: token case-insensitively', () => {
    const f = queryToFields('label:WORK', mailboxIdByName);
    expect(f.mailboxId).toBe('mb-work');
  });

  it('ignores unknown label names (mailboxId stays empty)', () => {
    const f = queryToFields('label:nonexistent', mailboxIdByName);
    expect(f.mailboxId).toBe('');
  });

  it('sets hasAttachment for has:attachment', () => {
    const f = queryToFields('has:attachment', mailboxIdByName);
    expect(f.hasAttachment).toBe(true);
  });

  it('ignores bare text tokens', () => {
    const f = queryToFields('hello world', mailboxIdByName);
    expect(f).toEqual(emptyFields());
  });

  it('parses a combined query', () => {
    const f = queryToFields('from:alice@x.test has:attachment', mailboxIdByName);
    expect(f.from).toBe('alice@x.test');
    expect(f.hasAttachment).toBe(true);
  });
});

describe('round-trip: fieldsToQuery -> queryToFields', () => {
  it('round-trips a full set of fields', () => {
    const original = {
      from: 'alice@x.test',
      to: 'bob@x.test',
      subject: 'weekly meeting',
      body: '',
      after: '2024-01-01',
      before: '2024-12-31',
      mailboxId: 'mb-work',
      hasAttachment: true,
    };
    const query = fieldsToQuery(original, mailboxNameById);
    const recovered = queryToFields(query, mailboxIdByName);
    expect(recovered.from).toBe(original.from);
    expect(recovered.to).toBe(original.to);
    expect(recovered.subject).toBe(original.subject);
    expect(recovered.after).toBe(original.after);
    expect(recovered.before).toBe(original.before);
    expect(recovered.mailboxId).toBe(original.mailboxId);
    expect(recovered.hasAttachment).toBe(original.hasAttachment);
  });
});
