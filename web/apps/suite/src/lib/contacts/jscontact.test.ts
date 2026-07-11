/**
 * Unit tests for jscontact.ts — Card <-> view-model mapping, merge-patch
 * builder, unknown-property preservation, and validation.
 *
 * All tests operate on the exported pure helpers via _internals_forTest.
 * The stateful stores (JMAP calls) are covered by integration tests.
 */

import { describe, it, expect } from 'vitest';
import {
  cardToVM,
  vmToCard,
  vmToPatch,
  validateVM,
  formatAddress,
  nextKey,
  type ContactEditVM,
  type NameVM,
  type EmailVM,
  type PhoneVM,
  type AddressVM,
  type OrgVM,
  type TitleVM,
  type NicknameVM,
  type LinkVM,
  type NoteVM,
  type AnniversaryVM,
} from './jscontact';

// ── Helpers ───────────────────────────────────────────────────────────────────

function emptyNameVM(): NameVM {
  return { given: '', surname: '', middle: '', prefix: '', suffix: '', credential: '', full: '', sortAs: '', isOrdered: false };
}

function emptyVM(): ContactEditVM {
  return {
    id: null,
    kind: 'individual',
    name: emptyNameVM(),
    emails: [],
    phones: [],
    addresses: [],
    orgs: [],
    titles: [],
    nicknames: [],
    links: [],
    notes: [],
    anniversaries: [],
    rawCard: {},
  };
}

function emailVM(key: string, address: string, pref = false): EmailVM {
  return { key, address, contextHome: false, contextWork: false, contextOther: false, pref, label: '' };
}

function phoneVM(key: string, number: string): PhoneVM {
  return { key, number, contextHome: false, contextWork: false, contextOther: false, featureVoice: false, featureCell: false, featureFax: false, featureText: false, featureVideo: false, pref: false, label: '' };
}

function addrVM(key: string, street: string, locality: string): AddressVM {
  return { key, street, locality, region: '', postcode: '', country: '', countryCode: '', contextHome: false, contextWork: false, contextOther: false, pref: false, label: '', full: '', extraComponents: [] };
}

// ── cardToVM: name parsing ────────────────────────────────────────────────────

describe('cardToVM: name', () => {
  it('parses given and surname from components', () => {
    const raw = {
      id: 'c1',
      name: {
        components: [
          { kind: 'given', value: 'Alice' },
          { kind: 'surname', value: 'Smith' },
        ],
      },
    };
    const vm = cardToVM(raw);
    expect(vm.name.given).toBe('Alice');
    expect(vm.name.surname).toBe('Smith');
  });

  it('parses full name override', () => {
    const raw = {
      id: 'c1',
      name: { full: 'Alice J. Smith', components: [] },
    };
    const vm = cardToVM(raw);
    expect(vm.name.full).toBe('Alice J. Smith');
  });

  it('parses prefix, middle, credential, suffix', () => {
    const raw = {
      id: 'c1',
      name: {
        components: [
          { kind: 'title', value: 'Dr.' },
          { kind: 'given', value: 'Alice' },
          { kind: 'given2', value: 'Jean' },
          { kind: 'surname', value: 'Smith' },
          { kind: 'credential', value: 'PhD' },
          { kind: 'generation', value: 'Jr.' },
        ],
      },
    };
    const vm = cardToVM(raw);
    expect(vm.name.prefix).toBe('Dr.');
    expect(vm.name.given).toBe('Alice');
    expect(vm.name.middle).toBe('Jean');
    expect(vm.name.surname).toBe('Smith');
    expect(vm.name.credential).toBe('PhD');
    expect(vm.name.suffix).toBe('Jr.');
  });

  it('defaults to empty name when name is absent', () => {
    const vm = cardToVM({ id: 'c1' });
    expect(vm.name.given).toBe('');
    expect(vm.name.surname).toBe('');
    expect(vm.name.full).toBe('');
  });
});

// ── cardToVM: emails ──────────────────────────────────────────────────────────

describe('cardToVM: emails', () => {
  it('parses email entries from a map', () => {
    const raw = {
      id: 'c1',
      emails: {
        work: { address: 'alice@work.com', contexts: { work: true }, pref: 1 },
        home: { address: 'alice@home.com', contexts: { home: true } },
      },
    };
    const vm = cardToVM(raw);
    // entries are sorted by key: "home" < "work"
    expect(vm.emails).toHaveLength(2);
    const home = vm.emails.find((e) => e.key === 'home');
    const work = vm.emails.find((e) => e.key === 'work');
    expect(home?.address).toBe('alice@home.com');
    expect(home?.contextHome).toBe(true);
    expect(home?.pref).toBe(false);
    expect(work?.address).toBe('alice@work.com');
    expect(work?.contextWork).toBe(true);
    expect(work?.pref).toBe(true); // pref: 1 => pref: true
  });
});

// ── cardToVM: phones ──────────────────────────────────────────────────────────

describe('cardToVM: phones', () => {
  it('parses phone features and contexts', () => {
    const raw = {
      id: 'c1',
      phones: {
        mobile: {
          number: '+49 123 456',
          features: { cell: true, voice: true },
          contexts: { home: true },
        },
      },
    };
    const vm = cardToVM(raw);
    expect(vm.phones).toHaveLength(1);
    const phone = vm.phones[0]!;
    expect(phone.number).toBe('+49 123 456');
    expect(phone.featureCell).toBe(true);
    expect(phone.featureVoice).toBe(true);
    expect(phone.contextHome).toBe(true);
  });
});

// ── cardToVM: addresses ───────────────────────────────────────────────────────

describe('cardToVM: addresses', () => {
  it('extracts common component kinds', () => {
    const raw = {
      id: 'c1',
      addresses: {
        home: {
          components: [
            { kind: 'name', value: '123 Main St' },
            { kind: 'locality', value: 'Springfield' },
            { kind: 'region', value: 'IL' },
            { kind: 'postcode', value: '62701' },
            { kind: 'country', value: 'USA' },
          ],
          contexts: { home: true },
          countryCode: 'US',
        },
      },
    };
    const vm = cardToVM(raw);
    expect(vm.addresses).toHaveLength(1);
    const a = vm.addresses[0]!;
    expect(a.street).toBe('123 Main St');
    expect(a.locality).toBe('Springfield');
    expect(a.region).toBe('IL');
    expect(a.postcode).toBe('62701');
    expect(a.country).toBe('USA');
    expect(a.countryCode).toBe('US');
    expect(a.contextHome).toBe(true);
  });

  it('preserves extra component kinds as extraComponents', () => {
    const raw = {
      id: 'c1',
      addresses: {
        home: {
          components: [
            { kind: 'name', value: 'Baker St' },
            { kind: 'building', value: 'Apt 2B' },  // unknown kind
            { kind: 'locality', value: 'London' },
          ],
        },
      },
    };
    const vm = cardToVM(raw);
    const addr0 = vm.addresses[0]!;
    expect(addr0.street).toBe('Baker St');
    expect(addr0.locality).toBe('London');
    expect(addr0.extraComponents).toHaveLength(1);
    expect(addr0.extraComponents[0]).toEqual({ kind: 'building', value: 'Apt 2B' });
  });
});

// ── cardToVM: orgs + titles ───────────────────────────────────────────────────

describe('cardToVM: organizations and titles', () => {
  it('parses org name and units', () => {
    const raw = {
      id: 'c1',
      organizations: {
        o1: { name: 'Acme Corp', units: ['Engineering', 'Backend'] },
      },
    };
    const vm = cardToVM(raw);
    expect(vm.orgs).toHaveLength(1);
    const org0 = vm.orgs[0]!;
    expect(org0.name).toBe('Acme Corp');
    expect(org0.units).toEqual(['Engineering', 'Backend']);
  });

  it('parses title with kind and organizationId', () => {
    const raw = {
      id: 'c1',
      titles: {
        t1: { name: 'Senior Engineer', kind: 'title', organizationId: 'o1' },
      },
    };
    const vm = cardToVM(raw);
    expect(vm.titles).toHaveLength(1);
    const title0 = vm.titles[0]!;
    expect(title0.name).toBe('Senior Engineer');
    expect(title0.kind).toBe('title');
    expect(title0.orgId).toBe('o1');
  });
});

// ── cardToVM: untyped properties ──────────────────────────────────────────────

describe('cardToVM: untyped properties (nicknames/links/notes/anniversaries)', () => {
  it('parses nicknames', () => {
    const raw = {
      id: 'c1',
      nicknames: { nn1: { name: 'Al' } },
    };
    const vm = cardToVM(raw);
    expect(vm.nicknames).toHaveLength(1);
    expect(vm.nicknames[0]!.name).toBe('Al');
  });

  it('parses links', () => {
    const raw = {
      id: 'c1',
      links: { web: { uri: 'https://example.com', kind: 'uri', label: 'Website' } },
    };
    const vm = cardToVM(raw);
    expect(vm.links).toHaveLength(1);
    const link0 = vm.links[0]!;
    expect(link0.uri).toBe('https://example.com');
    expect(link0.kind).toBe('uri');
    expect(link0.label).toBe('Website');
  });

  it('parses notes', () => {
    const raw = {
      id: 'c1',
      notes: { n1: { note: 'Met at conference 2024.' } },
    };
    const vm = cardToVM(raw);
    expect(vm.notes).toHaveLength(1);
    expect(vm.notes[0]!.note).toBe('Met at conference 2024.');
  });

  it('parses anniversaries with PartialDate', () => {
    const raw = {
      id: 'c1',
      anniversaries: {
        bday: { kind: 'birth', date: { '@type': 'PartialDate', year: 1990, month: 7, day: 15 } },
      },
    };
    const vm = cardToVM(raw);
    expect(vm.anniversaries).toHaveLength(1);
    const ann0 = vm.anniversaries[0]!;
    expect(ann0.kind).toBe('birth');
    expect(ann0.year).toBe('1990');
    expect(ann0.month).toBe('7');
    expect(ann0.day).toBe('15');
  });

  it('parses anniversaries with year absent (partial date)', () => {
    const raw = {
      id: 'c1',
      anniversaries: {
        bday: { kind: 'birth', date: { '@type': 'PartialDate', month: 3, day: 21 } },
      },
    };
    const vm = cardToVM(raw);
    const pann = vm.anniversaries[0]!;
    expect(pann.year).toBe('');
    expect(pann.month).toBe('3');
    expect(pann.day).toBe('21');
  });
});

// ── vmToCard ──────────────────────────────────────────────────────────────────

describe('vmToCard', () => {
  it('emits version 1.0 and kind individual by default', () => {
    const vm = emptyVM();
    vm.name.given = 'Alice';
    const card = vmToCard(vm);
    expect(card.version).toBe('1.0');
    expect(card.kind).toBe('individual');
  });

  it('does not emit uid (server mints it, REQ-CONT-02)', () => {
    const vm = emptyVM();
    vm.name.given = 'Alice';
    const card = vmToCard(vm);
    expect('uid' in card).toBe(false);
  });

  it('builds name.components in correct order', () => {
    const vm = emptyVM();
    vm.name.prefix = 'Dr.';
    vm.name.given = 'Alice';
    vm.name.middle = 'Jean';
    vm.name.surname = 'Smith';
    vm.name.credential = 'PhD';
    vm.name.suffix = 'Jr.';
    const card = vmToCard(vm);
    const comps = (card.name as Record<string, unknown>).components as Array<{kind: string; value: string}>;
    expect(comps.map((c) => c.kind)).toEqual(['title', 'given', 'given2', 'surname', 'credential', 'generation']);
  });

  it('builds email map with pref and contexts', () => {
    const vm = emptyVM();
    vm.name.given = 'Alice';
    vm.emails = [
      { key: 'home', address: 'a@home.com', contextHome: true, contextWork: false, contextOther: false, pref: true, label: '' },
      { key: 'work', address: 'a@work.com', contextHome: false, contextWork: true, contextOther: false, pref: false, label: '' },
    ];
    const card = vmToCard(vm);
    const emails = card.emails as Record<string, Record<string, unknown>>;
    const homeEmail = emails.home!;
    const workEmail = emails.work!;
    expect(homeEmail.address).toBe('a@home.com');
    expect(homeEmail.pref).toBe(1);
    expect((homeEmail.contexts as Record<string, boolean>).home).toBe(true);
    expect('pref' in workEmail).toBe(false);
  });

  it('omits a section when all entries are empty', () => {
    const vm = emptyVM();
    vm.name.given = 'Alice';
    vm.emails = [{ key: 'e1', address: '', contextHome: false, contextWork: false, contextOther: false, pref: false, label: '' }];
    const card = vmToCard(vm);
    expect(card.emails).toBeUndefined();
  });

  it('builds anniversary with PartialDate', () => {
    const vm = emptyVM();
    vm.name.given = 'Alice';
    vm.anniversaries = [{ key: 'bday', kind: 'birth', year: '1990', month: '7', day: '15' }];
    const card = vmToCard(vm);
    const ann = (card.anniversaries as Record<string, unknown>).bday as Record<string, unknown>;
    expect(ann.kind).toBe('birth');
    const date = ann.date as Record<string, unknown>;
    expect(date['@type']).toBe('PartialDate');
    expect(date.year).toBe(1990);
    expect(date.month).toBe(7);
    expect(date.day).toBe(15);
  });

  it('builds anniversary without year', () => {
    const vm = emptyVM();
    vm.name.given = 'Alice';
    vm.anniversaries = [{ key: 'bday', kind: 'birth', year: '', month: '12', day: '25' }];
    const card = vmToCard(vm);
    const ann = (card.anniversaries as Record<string, unknown>).bday as Record<string, unknown>;
    const date = ann.date as Record<string, unknown>;
    expect('year' in date).toBe(false);
    expect(date.month).toBe(12);
    expect(date.day).toBe(25);
  });

  it('builds address with extra components preserved', () => {
    const vm = emptyVM();
    vm.name.given = 'Alice';
    vm.addresses = [{
      key: 'home',
      street: 'Baker St',
      locality: 'London',
      region: '', postcode: '', country: '',
      countryCode: 'GB',
      contextHome: true, contextWork: false, contextOther: false,
      pref: false, label: '', full: '',
      extraComponents: [{ kind: 'building', value: 'Suite 2' }],
    }];
    const card = vmToCard(vm);
    const addr = (card.addresses as Record<string, unknown>).home as Record<string, unknown>;
    const comps = addr.components as Array<{kind: string; value: string}>;
    expect(comps.find((c) => c.kind === 'name')?.value).toBe('Baker St');
    expect(comps.find((c) => c.kind === 'locality')?.value).toBe('London');
    expect(comps.find((c) => c.kind === 'building')?.value).toBe('Suite 2');
  });
});

// ── vmToPatch: unknown-property preservation ──────────────────────────────────

describe('vmToPatch: unknown-property preservation (REQ-CONT-01)', () => {
  it('does not include unchanged managed keys in the patch', () => {
    const originalRaw = {
      id: 'c1',
      version: '1.0',
      kind: 'individual',
      name: {
        components: [{ kind: 'given', value: 'Alice' }],
      },
      emails: { e1: { address: 'alice@example.com' } },
    };
    const vm = cardToVM(originalRaw);
    // No changes to the VM.
    const { changed, patch } = vmToPatch(originalRaw, vm);
    expect(changed).toBe(false);
    expect(Object.keys(patch)).toHaveLength(0);
  });

  it('only includes the changed field in the patch', () => {
    const originalRaw = {
      id: 'c1',
      version: '1.0',
      name: { components: [{ kind: 'given', value: 'Alice' }] },
      emails: { e1: { address: 'alice@example.com' } },
      // unknown property — should NOT appear in patch
      customField: { someValue: 42 },
    };
    const vm = cardToVM(originalRaw);
    vm.notes.push({ key: 'n1', note: 'A new note.' });
    const { changed, patch } = vmToPatch(originalRaw, vm);
    expect(changed).toBe(true);
    // Only 'notes' should appear in the patch; 'customField' must not.
    expect(Object.keys(patch)).toEqual(['notes']);
    expect('customField' in patch).toBe(false);
  });

  it('sets a managed key to null in the patch when removing it', () => {
    const originalRaw = {
      id: 'c1',
      name: { components: [{ kind: 'given', value: 'Alice' }] },
      phones: { p1: { number: '+1 234 5678' } },
    };
    const vm = cardToVM(originalRaw);
    // Remove the only phone.
    vm.phones = [];
    const { patch } = vmToPatch(originalRaw, vm);
    expect(patch.phones).toBeNull();
  });

  it('preserves unknown top-level properties by not including them in the patch', () => {
    const originalRaw = {
      id: 'c1',
      name: { components: [{ kind: 'given', value: 'Alice' }] },
      // RFC 9553 property the form doesn't edit — must not touch.
      'urn:example.com:foo': { value: 'bar' },
    };
    const vm = cardToVM(originalRaw);
    vm.name.surname = 'Smith';
    const { patch } = vmToPatch(originalRaw, vm);
    expect('urn:example.com:foo' in patch).toBe(false);
  });
});

// ── validateVM ────────────────────────────────────────────────────────────────

describe('validateVM', () => {
  it('passes when a name is present', () => {
    const vm = emptyVM();
    vm.name.given = 'Alice';
    expect(validateVM(vm)).toHaveLength(0);
  });

  it('passes when only an email is present', () => {
    const vm = emptyVM();
    vm.emails = [emailVM('e1', 'alice@example.com')];
    expect(validateVM(vm)).toHaveLength(0);
  });

  it('passes when only a phone is present', () => {
    const vm = emptyVM();
    vm.phones = [phoneVM('p1', '+49 123')];
    expect(validateVM(vm)).toHaveLength(0);
  });

  it('fails when the card is empty', () => {
    const vm = emptyVM();
    const errs = validateVM(vm);
    expect(errs.some((e) => e.field === 'general')).toBe(true);
  });

  it('rejects malformed email addresses', () => {
    const vm = emptyVM();
    vm.name.given = 'Alice';
    vm.emails = [emailVM('e1', 'not-an-email')];
    const errs = validateVM(vm);
    expect(errs.some((e) => e.field === 'email.0')).toBe(true);
  });

  it('accepts valid email addresses', () => {
    const vm = emptyVM();
    vm.name.given = 'Alice';
    vm.emails = [emailVM('e1', 'alice@example.com')];
    expect(validateVM(vm)).toHaveLength(0);
  });

  it('rejects more than one preferred email', () => {
    const vm = emptyVM();
    vm.name.given = 'Alice';
    vm.emails = [emailVM('e1', 'a@a.com', true), emailVM('e2', 'b@b.com', true)];
    const errs = validateVM(vm);
    expect(errs.some((e) => e.field === 'emails.pref')).toBe(true);
  });

  it('accepts exactly one preferred email', () => {
    const vm = emptyVM();
    vm.name.given = 'Alice';
    vm.emails = [emailVM('e1', 'a@a.com', true), emailVM('e2', 'b@b.com', false)];
    expect(validateVM(vm)).toHaveLength(0);
  });
});

// ── formatAddress ─────────────────────────────────────────────────────────────

describe('formatAddress', () => {
  it('formats street + locality + region + country', () => {
    const a = addrVM('h', '123 Main St', 'Springfield');
    a.region = 'IL';
    a.postcode = '62701';
    a.country = 'USA';
    expect(formatAddress(a)).toBe('123 Main St, 62701 Springfield, IL, USA');
  });

  it('falls back to full when components are absent', () => {
    const a = addrVM('h', '', '');
    a.full = '5 Bakerstreet, London';
    expect(formatAddress(a)).toBe('5 Bakerstreet, London');
  });
});

// ── nextKey ───────────────────────────────────────────────────────────────────

describe('nextKey', () => {
  it('returns distinct keys on each call', () => {
    const k1 = nextKey();
    const k2 = nextKey();
    const k3 = nextKey();
    expect(k1).not.toBe(k2);
    expect(k2).not.toBe(k3);
  });
});

// ── round-trip fidelity ───────────────────────────────────────────────────────

describe('round-trip fidelity', () => {
  it('card -> VM -> card preserves all managed fields', () => {
    const originalRaw = {
      id: 'c1',
      version: '1.0',
      kind: 'individual',
      name: {
        full: 'Alice Smith',
        components: [
          { kind: 'given', value: 'Alice' },
          { kind: 'surname', value: 'Smith' },
        ],
      },
      emails: {
        primary: { address: 'alice@example.com', pref: 1 },
        work: { address: 'alice@work.com', contexts: { work: true } },
      },
      phones: {
        mobile: { number: '+49 123', features: { cell: true }, pref: 1 },
      },
      organizations: {
        o1: { name: 'Acme Corp', units: ['Engineering'] },
      },
      titles: {
        t1: { name: 'Software Engineer', kind: 'title', organizationId: 'o1' },
      },
      nicknames: { nn1: { name: 'Al' } },
      links: { web: { uri: 'https://alice.example.com', kind: 'uri' } },
      notes: { n1: { note: 'Met at a conference.' } },
      anniversaries: {
        bday: { kind: 'birth', date: { '@type': 'PartialDate', year: 1990, month: 7, day: 15 } },
      },
    };

    const vm = cardToVM(originalRaw);
    const { changed } = vmToPatch(originalRaw, vm);
    // No changes made to the VM, so the patch should be empty.
    expect(changed).toBe(false);
  });
});
