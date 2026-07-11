/**
 * JSContact Card types and client-side helpers (RFC 9553).
 *
 * Provides:
 *   - TypeScript types for the JSContact wire format
 *   - A rich view-model (ContactEditVM) for the edit/create form
 *   - cardToVM: raw Card object -> ContactEditVM
 *   - vmToCard: ContactEditVM -> Card object ready for Contact/set create
 *   - vmToPatch: ContactEditVM + original raw -> JSON Merge Patch for update
 *   - validateVM: client-side validation before save
 *   - nextKey: generate a unique map entry key
 *
 * Unknown properties in the raw Card are preserved verbatim on update
 * because vmToPatch only emits the top-level keys it manages. Any other
 * top-level keys in the original Card are untouched by the patch
 * (JSON Merge Patch semantics: omitted keys are preserved).
 *
 * REQ-CONT-01: unknown properties round-trip intact.
 * REQ-CONT-02: uid is not fabricated on create; version is "1.0".
 */

// ── Wire types (RFC 9553) ─────────────────────────────────────────────────────

export interface JsNameComponent {
  kind: string;  // given | surname | given2 | title | credential | generation | ...
  value: string;
}

export interface JsName {
  components?: JsNameComponent[];
  full?: string;
  isOrdered?: boolean;
  defaultSeparator?: string;
  sortAs?: Record<string, string>;
}

export interface JsEmailAddress {
  address: string;
  contexts?: Record<string, boolean>;
  pref?: number;
  label?: string;
}

export interface JsPhone {
  number: string;
  contexts?: Record<string, boolean>;
  features?: Record<string, boolean>;
  pref?: number;
  label?: string;
}

export interface JsAddressComponent {
  kind: string;  // name | number | locality | region | postcode | country | ...
  value: string;
}

export interface JsAddress {
  components?: JsAddressComponent[];
  full?: string;
  contexts?: Record<string, boolean>;
  pref?: number;
  label?: string;
  countryCode?: string;
  coordinates?: string;
  timeZone?: string;
}

export interface JsOrganization {
  name?: string;
  /** Array of unit name strings (server representation: []string). */
  units?: string[];
  sortAs?: string;
  contexts?: Record<string, boolean>;
}

export interface JsTitle {
  name?: string;
  kind?: string;
  organizationId?: string;
}

export interface JsNickname {
  name: string;
}

export interface JsLink {
  uri: string;
  kind?: string;
  rel?: string;
  mediaType?: string;
  label?: string;
  pref?: number;
  contexts?: Record<string, boolean>;
}

export interface JsMediaResource {
  '@type': 'MediaResource';
  kind?: string;
  blobId?: string;
  mediaType?: string;
  uri?: string;
  label?: string;
}

export interface JsNote {
  note: string;
  author?: unknown;
  created?: string;
}

export interface JsPartialDate {
  '@type': 'PartialDate';
  year?: number;
  month?: number;
  day?: number;
  calendarScale?: string;
}

export interface JsTimestamp {
  '@type': 'Timestamp';
  utc: string;
}

export type JsContactDate = JsPartialDate | JsTimestamp | Record<string, unknown>;

export interface JsAnniversary {
  kind: string;
  date: JsContactDate;
  place?: unknown;
}

/** Raw JSContact Card as returned by Contact/get. */
export interface JsCard {
  id: string;
  version?: string;
  kind?: string;
  uid?: string;
  name?: JsName;
  emails?: Record<string, JsEmailAddress>;
  phones?: Record<string, JsPhone>;
  addresses?: Record<string, JsAddress>;
  organizations?: Record<string, JsOrganization>;
  titles?: Record<string, JsTitle>;
  nicknames?: Record<string, JsNickname>;
  links?: Record<string, JsLink>;
  notes?: Record<string, JsNote>;
  anniversaries?: Record<string, JsAnniversary>;
  /**
   * Contact photos and other media resources (JSContact media map).
   * Photo entries carry kind="photo" and a blobId referencing the JMAP blob.
   * REQ-CONT-60.
   */
  media?: Record<string, JsMediaResource>;
  /**
   * Group membership: present only when kind="group".
   * Keys are JMAP contact IDs (NOT UIDs); values are always true.
   * Confirmed wire format via round-trip (2026-07-11).
   */
  members?: Record<string, boolean>;
  [key: string]: unknown;
}

// ── View model for the edit/create form ──────────────────────────────────────

export interface NameVM {
  given: string;
  surname: string;
  /** middle name (kind "given2") */
  middle: string;
  /** honorific prefix (kind "title") */
  prefix: string;
  /** generation suffix (kind "generation") */
  suffix: string;
  credential: string;
  /** explicit full-name override; "" means derived from components */
  full: string;
  /** explicit sort key; "" means derived */
  sortAs: string;
  isOrdered: boolean;
}

export interface EmailVM {
  key: string;
  address: string;
  contextHome: boolean;
  contextWork: boolean;
  contextOther: boolean;
  /** maps to pref: 1 when true */
  pref: boolean;
  label: string;
}

export interface PhoneVM {
  key: string;
  number: string;
  contextHome: boolean;
  contextWork: boolean;
  contextOther: boolean;
  featureVoice: boolean;
  featureCell: boolean;
  featureFax: boolean;
  featureText: boolean;
  featureVideo: boolean;
  pref: boolean;
  label: string;
}

export interface AddressVM {
  key: string;
  /** component kind "name" (street name/number) */
  street: string;
  /** component kind "locality" */
  locality: string;
  /** component kind "region" */
  region: string;
  /** component kind "postcode" */
  postcode: string;
  /** component kind "country" */
  country: string;
  countryCode: string;
  contextHome: boolean;
  contextWork: boolean;
  contextOther: boolean;
  pref: boolean;
  label: string;
  /** full fallback string */
  full: string;
  /** components with kinds we don't edit — preserved on save */
  extraComponents: JsAddressComponent[];
}

export interface OrgVM {
  key: string;
  name: string;
  units: string[];
}

export interface TitleVM {
  key: string;
  name: string;
  /** "title" | "role" | "" (empty = no kind set) */
  kind: string;
  orgId: string;
}

export interface NicknameVM {
  key: string;
  name: string;
}

export interface LinkVM {
  key: string;
  uri: string;
  kind: string;
  label: string;
}

export interface NoteVM {
  key: string;
  note: string;
}

export interface AnniversaryVM {
  key: string;
  /** "birth" | "wedding" | other free string */
  kind: string;
  /** "" if year not set */
  year: string;
  /** "1".."12" */
  month: string;
  /** "1".."31" */
  day: string;
}

export interface ContactEditVM {
  /** null for new contact (create); id string for existing (update) */
  id: string | null;
  kind: string;
  name: NameVM;
  emails: EmailVM[];
  phones: PhoneVM[];
  addresses: AddressVM[];
  orgs: OrgVM[];
  titles: TitleVM[];
  nicknames: NicknameVM[];
  links: LinkVM[];
  notes: NoteVM[];
  anniversaries: AnniversaryVM[];
  /**
   * The full original raw Card object (as received from Contact/get).
   * Used by vmToPatch to preserve unknown top-level properties — they
   * are never included in the patch so the server leaves them alone.
   */
  rawCard: Record<string, unknown>;
}

export interface ValidationError {
  /** Dot-path into the form, e.g. "email.2.address" or "general". */
  field: string;
  message: string;
}

// ── Key generation ────────────────────────────────────────────────────────────

let _keyCounter = 0;

/** Generate a unique map entry key for a new repeatable-list entry. */
export function nextKey(): string {
  return 'n' + (++_keyCounter);
}

// ── Managed top-level keys (the ones the form edits) ─────────────────────────

const MANAGED_KEYS = [
  'kind',
  'name',
  'emails',
  'phones',
  'addresses',
  'organizations',
  'titles',
  'nicknames',
  'links',
  'notes',
  'anniversaries',
] as const;

// ── cardToVM ──────────────────────────────────────────────────────────────────

/** Convert a raw JSContact Card (from Contact/get) to a form view model. */
export function cardToVM(raw: Record<string, unknown>): ContactEditVM {
  const id = typeof raw.id === 'string' ? raw.id : null;
  const kind = typeof raw.kind === 'string' ? raw.kind : 'individual';

  const name = parseNameVM(raw.name as Record<string, unknown> | undefined);

  const emails = parseMapVM<JsEmailAddress, EmailVM>(
    raw.emails as Record<string, unknown> | undefined,
    (key, e) => ({
      key,
      address: String(e.address ?? ''),
      contextHome: !!(e.contexts?.['home']),
      contextWork: !!(e.contexts?.['work']),
      contextOther: !!(e.contexts?.['other']),
      pref: typeof e.pref === 'number' && e.pref > 0,
      label: String(e.label ?? ''),
    }),
  );

  const phones = parseMapVM<JsPhone, PhoneVM>(
    raw.phones as Record<string, unknown> | undefined,
    (key, p) => ({
      key,
      number: String(p.number ?? ''),
      contextHome: !!(p.contexts?.['home']),
      contextWork: !!(p.contexts?.['work']),
      contextOther: !!(p.contexts?.['other']),
      featureVoice: !!(p.features?.['voice']),
      featureCell: !!(p.features?.['cell']),
      featureFax: !!(p.features?.['fax']),
      featureText: !!(p.features?.['text']),
      featureVideo: !!(p.features?.['video']),
      pref: typeof p.pref === 'number' && p.pref > 0,
      label: String(p.label ?? ''),
    }),
  );

  const addresses = parseMapVM<JsAddress, AddressVM>(
    raw.addresses as Record<string, unknown> | undefined,
    (key, a) => parseAddressVM(key, a),
  );

  const orgs = parseMapVM<JsOrganization, OrgVM>(
    raw.organizations as Record<string, unknown> | undefined,
    (key, o) => ({
      key,
      name: String(o.name ?? ''),
      units: Array.isArray(o.units)
        ? o.units.map((u) => (typeof u === 'string' ? u : String(u)))
        : [],
    }),
  );

  const titles = parseMapVM<JsTitle, TitleVM>(
    raw.titles as Record<string, unknown> | undefined,
    (key, t) => ({
      key,
      name: String(t.name ?? ''),
      kind: typeof t.kind === 'string' ? t.kind : '',
      orgId: typeof t.organizationId === 'string' ? t.organizationId : '',
    }),
  );

  const nicknames = parseMapVM<JsNickname, NicknameVM>(
    raw.nicknames as Record<string, unknown> | undefined,
    (key, n) => ({ key, name: String(n.name ?? '') }),
  );

  const links = parseMapVM<JsLink, LinkVM>(
    raw.links as Record<string, unknown> | undefined,
    (key, l) => ({
      key,
      uri: String(l.uri ?? ''),
      kind: String(l.kind ?? ''),
      label: String(l.label ?? ''),
    }),
  );

  const notes = parseMapVM<JsNote, NoteVM>(
    raw.notes as Record<string, unknown> | undefined,
    (key, n) => ({ key, note: String(n.note ?? '') }),
  );

  const anniversaries = parseMapVM<JsAnniversary, AnniversaryVM>(
    raw.anniversaries as Record<string, unknown> | undefined,
    (key, a) => parseAnniversaryVM(key, a),
  );

  return {
    id,
    kind,
    name,
    emails,
    phones,
    addresses,
    orgs,
    titles,
    nicknames,
    links,
    notes,
    anniversaries,
    rawCard: raw,
  };
}

function parseNameVM(name: Record<string, unknown> | undefined): NameVM {
  if (!name) {
    return { given: '', surname: '', middle: '', prefix: '', suffix: '', credential: '', full: '', sortAs: '', isOrdered: false };
  }
  let given = '';
  let surname = '';
  let middle = '';
  let prefix = '';
  let suffix = '';
  let credential = '';
  if (Array.isArray(name.components)) {
    for (const c of name.components) {
      if (typeof c !== 'object' || c === null) continue;
      const comp = c as Record<string, unknown>;
      const v = String(comp.value ?? '');
      switch (comp.kind) {
        case 'given':      given = v; break;
        case 'surname':    surname = v; break;
        case 'given2':     middle = v; break;
        case 'title':      prefix = v; break;
        case 'generation': suffix = v; break;
        case 'credential': credential = v; break;
      }
    }
  }
  const full = String(name.full ?? '');
  // Fall back to deriving given/surname from `full` when the Card carries no
  // recognized given/surname component (e.g. a component with an empty or
  // unrecognized `kind`, written by an older buggy write path). This lets the
  // edit form populate Vorname/Nachname for such contacts without a server-side
  // data migration.
  if (!given && !surname && full.trim()) {
    const split = splitDisplayName(full);
    given = split.given;
    surname = split.surname;
  }
  const sortAsMap = name.sortAs as Record<string, string> | undefined;
  const sortAs = sortAsMap ? (sortAsMap['surname'] ?? sortAsMap['given'] ?? '') : '';
  return {
    given,
    surname,
    middle,
    prefix,
    suffix,
    credential,
    full,
    sortAs,
    isOrdered: !!(name.isOrdered),
  };
}

/**
 * Split a single display-name string into given/surname parts: the first
 * whitespace-separated token becomes the given name, the remainder (if any)
 * becomes the surname. Used both as a name-parsing fallback (REQ-CONT-01)
 * and by contact-creation call sites that only have a display name on hand
 * (mail sender "Add contact", compose recipient "Save to contacts").
 */
export function splitDisplayName(displayName: string): { given: string; surname: string } {
  const parts = displayName.trim().split(/\s+/);
  if (parts.length <= 1) return { given: parts[0] ?? '', surname: '' };
  return { given: parts[0]!, surname: parts.slice(1).join(' ') };
}

function parseAddressVM(key: string, a: JsAddress): AddressVM {
  let street = '';
  let locality = '';
  let region = '';
  let postcode = '';
  let country = '';
  const extraComponents: JsAddressComponent[] = [];
  if (Array.isArray(a.components)) {
    for (const c of a.components) {
      // a.components is JsAddressComponent[] so c is already typed.
      const kind = c.kind;
      const v = c.value;
      switch (kind) {
        case 'name':     street = street ? street + ' ' + v : v; break;
        case 'number':   street = street ? street + ' ' + v : v; break;
        case 'locality': locality = v; break;
        case 'region':   region = v; break;
        case 'postcode': postcode = v; break;
        case 'country':  country = v; break;
        default:
          extraComponents.push({ kind, value: v });
      }
    }
  }
  return {
    key,
    street,
    locality,
    region,
    postcode,
    country,
    countryCode: String(a.countryCode ?? ''),
    contextHome: !!(a.contexts?.['home']),
    contextWork: !!(a.contexts?.['work']),
    contextOther: !!(a.contexts?.['other']),
    pref: typeof a.pref === 'number' && a.pref > 0,
    label: String(a.label ?? ''),
    full: String(a.full ?? ''),
    extraComponents,
  };
}

function parseAnniversaryVM(key: string, a: JsAnniversary): AnniversaryVM {
  const kind = String(a.kind ?? 'birth');
  const date = a.date as Record<string, unknown> | undefined;
  let year = '';
  let month = '';
  let day = '';
  if (date && typeof date === 'object') {
    const t = date['@type'];
    if (t === 'PartialDate' || t === undefined) {
      // Accept PartialDate or bare date objects
      if (typeof date.year === 'number') year = String(date.year);
      if (typeof date.month === 'number') month = String(date.month);
      if (typeof date.day === 'number') day = String(date.day);
    } else if (t === 'Timestamp' && typeof date.utc === 'string') {
      // Parse ISO date from timestamp
      const d = new Date(date.utc);
      if (!isNaN(d.getTime())) {
        year = String(d.getUTCFullYear());
        month = String(d.getUTCMonth() + 1);
        day = String(d.getUTCDate());
      }
    }
  }
  return { key, kind, year, month, day };
}

/** Parse a JSContact map into a sorted array using the provided mapper. */
function parseMapVM<W, V>(
  raw: Record<string, unknown> | undefined,
  mapper: (key: string, val: W) => V | null,
): V[] {
  if (!raw || typeof raw !== 'object') return [];
  const keys = Object.keys(raw).sort();
  const result: V[] = [];
  for (const k of keys) {
    const entry = mapper(k, raw[k] as W);
    if (entry !== null) result.push(entry);
  }
  return result;
}

// ── vmToCard (for Contact/set create) ────────────────────────────────────────

/**
 * Build a Card object from a view model for Contact/set create.
 * Does NOT include uid (server mints it). REQ-CONT-02.
 */
export function vmToCard(vm: ContactEditVM): Record<string, unknown> {
  const card: Record<string, unknown> = {
    version: '1.0',
    kind: vm.kind || 'individual',
  };

  const name = buildName(vm.name);
  if (name) card.name = name;

  const emails = buildEmailMap(vm.emails);
  if (emails) card.emails = emails;

  const phones = buildPhoneMap(vm.phones);
  if (phones) card.phones = phones;

  const addresses = buildAddressMap(vm.addresses);
  if (addresses) card.addresses = addresses;

  const organizations = buildOrgMap(vm.orgs);
  if (organizations) card.organizations = organizations;

  const titles = buildTitleMap(vm.titles);
  if (titles) card.titles = titles;

  const nicknames = buildNicknameMap(vm.nicknames);
  if (nicknames) card.nicknames = nicknames;

  const links = buildLinkMap(vm.links);
  if (links) card.links = links;

  const notes = buildNoteMap(vm.notes);
  if (notes) card.notes = notes;

  const anniversaries = buildAnniversaryMap(vm.anniversaries);
  if (anniversaries) card.anniversaries = anniversaries;

  return card;
}

// ── vmToPatch helpers ─────────────────────────────────────────────────────────

/**
 * Sort object keys recursively so that JSON.stringify produces a stable,
 * key-order-independent string. Arrays preserve their element order.
 */
function canonicalize(val: unknown): unknown {
  if (val === null || typeof val !== 'object') return val;
  if (Array.isArray(val)) return val.map(canonicalize);
  const obj = val as Record<string, unknown>;
  const sorted: Record<string, unknown> = {};
  for (const k of Object.keys(obj).sort()) {
    sorted[k] = canonicalize(obj[k]);
  }
  return sorted;
}

/**
 * Normalize a field value before comparison so that a missing value in the
 * original card compares equal to the default the form would produce.
 *
 * - `kind`: RFC 9553 section 2.1 default is "individual"; treat null as
 *   'individual' so we never produce a spurious kind patch when the original
 *   omits the field.
 */
function normalizeForCompare(key: string, val: unknown): unknown {
  if (key === 'kind' && (val === null || val === undefined)) return 'individual';
  return val;
}

// ── vmToPatch (for Contact/set update) ───────────────────────────────────────

/**
 * Build a JSON Merge Patch from a view model and the original raw Card.
 * Only includes top-level keys that changed. Untouched keys (including
 * unknown properties) are absent from the patch and thus preserved by the
 * server's merge-patch semantics. REQ-CONT-01.
 *
 * Returns { changed: boolean, patch: Record<string, unknown> }.
 */
export function vmToPatch(
  originalRaw: Record<string, unknown>,
  vm: ContactEditVM,
): { changed: boolean; patch: Record<string, unknown> } {
  const newCard = vmToCard(vm);
  const patch: Record<string, unknown> = {};

  for (const key of MANAGED_KEYS) {
    const rawNewVal = (newCard[key as string] as unknown) ?? null;
    const origNorm = normalizeForCompare(key, originalRaw[key] ?? null);
    const newNorm = normalizeForCompare(key, rawNewVal);
    if (JSON.stringify(canonicalize(origNorm)) !== JSON.stringify(canonicalize(newNorm))) {
      // null in a merge patch means "delete this key"
      patch[key] = rawNewVal;
    }
  }

  return { changed: Object.keys(patch).length > 0, patch };
}

// ── validateVM ────────────────────────────────────────────────────────────────

/** Validate the view model before save. Returns [] on success. REQ-CONT-50. */
export function validateVM(vm: ContactEditVM): ValidationError[] {
  const errors: ValidationError[] = [];

  // Non-empty check: need at least a name component, email, or phone.
  const hasName =
    vm.name.given.trim() ||
    vm.name.surname.trim() ||
    vm.name.full.trim();
  const hasEmail = vm.emails.some((e) => e.address.trim());
  const hasPhone = vm.phones.some((p) => p.number.trim());
  if (!hasName && !hasEmail && !hasPhone) {
    errors.push({
      field: 'general',
      message: 'A contact must have at least a name, email address, or phone number.',
    });
  }

  // Email address syntax.
  for (let i = 0; i < vm.emails.length; i++) {
    const entry = vm.emails[i];
    if (!entry) continue;
    const addr = entry.address.trim();
    if (addr && !isValidEmail(addr)) {
      errors.push({ field: `email.${i}`, message: `Not a valid email address: ${addr}` });
    }
  }

  // At most one preferred per multi-valued property.
  const prefCount = (items: Array<{ pref: boolean }>) =>
    items.filter((x) => x.pref).length;

  if (prefCount(vm.emails) > 1) {
    errors.push({ field: 'emails.pref', message: 'At most one email can be preferred.' });
  }
  if (prefCount(vm.phones) > 1) {
    errors.push({ field: 'phones.pref', message: 'At most one phone can be preferred.' });
  }
  if (prefCount(vm.addresses) > 1) {
    errors.push({ field: 'addresses.pref', message: 'At most one address can be preferred.' });
  }

  return errors;
}

// ── Display helpers ───────────────────────────────────────────────────────────

/** Derive a display name from the view model. */
export function vmDisplayName(vm: ContactEditVM): string {
  if (vm.name.full.trim()) return vm.name.full.trim();
  const parts: string[] = [];
  if (vm.name.given.trim()) parts.push(vm.name.given.trim());
  if (vm.name.surname.trim()) parts.push(vm.name.surname.trim());
  if (parts.length > 0) return parts.join(' ');
  // Fallback: primary email or first org name.
  const email = vm.emails.find((e) => e.address.trim());
  if (email) return email.address.trim();
  const org = vm.orgs.find((o) => o.name.trim());
  if (org) return org.name.trim();
  return '';
}

/**
 * Format a postal address from its components for display.
 * Returns the full field if components are absent.
 */
export function formatAddress(addr: AddressVM): string {
  const lines: string[] = [];
  if (addr.street.trim()) lines.push(addr.street.trim());
  const cityLine = [addr.postcode.trim(), addr.locality.trim()]
    .filter(Boolean)
    .join(' ');
  if (cityLine) lines.push(cityLine);
  if (addr.region.trim()) lines.push(addr.region.trim());
  if (addr.country.trim()) lines.push(addr.country.trim());
  if (lines.length > 0) return lines.join(', ');
  return addr.full.trim();
}

/**
 * Format an anniversary for display (birthday, wedding, etc.).
 */
export function formatAnniversary(a: AnniversaryVM, locale: string): string {
  const year = parseInt(a.year, 10);
  const month = parseInt(a.month, 10);
  const day = parseInt(a.day, 10);
  if (!month && !day) return '';
  try {
    const opts: Intl.DateTimeFormatOptions = {
      month: 'long',
      day: 'numeric',
    };
    if (a.year && !isNaN(year)) opts.year = 'numeric';
    // Use a known year to allow date formatting without the year if needed.
    const effectiveYear = isNaN(year) ? 2000 : year;
    const d = new Date(effectiveYear, month - 1, day);
    return new Intl.DateTimeFormat(locale, opts).format(d);
  } catch {
    return [a.day, a.month, a.year].filter(Boolean).join('.');
  }
}

/**
 * Derive the monogram initial for avatar fallback.
 * Uses first char of given name, then surname, then display name.
 */
export function vmMonogram(vm: ContactEditVM): string {
  const g = vm.name.given.trim();
  if (g) return g.charAt(0).toUpperCase();
  const s = vm.name.surname.trim();
  if (s) return s.charAt(0).toUpperCase();
  const dn = vmDisplayName(vm);
  for (const ch of dn) {
    if (ch.trim()) return ch.toUpperCase();
  }
  return '?';
}

/** Monogram from a raw Card object (for use in the detail view directly). */
export function rawMonogram(raw: Record<string, unknown>): string {
  const vm = cardToVM(raw);
  return vmMonogram(vm);
}

// ── Internal builders ─────────────────────────────────────────────────────────

function buildName(n: NameVM): Record<string, unknown> | null {
  const components: JsNameComponent[] = [];
  if (n.prefix.trim()) components.push({ kind: 'title', value: n.prefix.trim() });
  if (n.given.trim()) components.push({ kind: 'given', value: n.given.trim() });
  if (n.middle.trim()) components.push({ kind: 'given2', value: n.middle.trim() });
  if (n.surname.trim()) components.push({ kind: 'surname', value: n.surname.trim() });
  if (n.credential.trim()) components.push({ kind: 'credential', value: n.credential.trim() });
  if (n.suffix.trim()) components.push({ kind: 'generation', value: n.suffix.trim() });

  if (components.length === 0 && !n.full.trim()) return null;

  const nameObj: Record<string, unknown> = {};
  if (components.length > 0) nameObj.components = components;
  if (n.full.trim()) nameObj.full = n.full.trim();
  if (n.isOrdered) nameObj.isOrdered = true;
  if (n.sortAs.trim()) {
    // Store sortAs keyed by the first component kind as convention.
    const first = components.length > 0 ? components[0] : undefined;
    const firstKind: string = first?.kind ?? 'surname';
    nameObj.sortAs = { [firstKind]: n.sortAs.trim() };
  }
  return nameObj;
}

function buildContexts(home: boolean, work: boolean, other: boolean): Record<string, boolean> | undefined {
  const c: Record<string, boolean> = {};
  if (home) c.home = true;
  if (work) c.work = true;
  if (other) c.other = true;
  return Object.keys(c).length > 0 ? c : undefined;
}

function buildEmailMap(emails: EmailVM[]): Record<string, unknown> | null {
  const valid = emails.filter((e) => e.address.trim());
  if (valid.length === 0) return null;
  const map: Record<string, unknown> = {};
  for (const e of valid) {
    const entry: Record<string, unknown> = { address: e.address.trim() };
    const ctx = buildContexts(e.contextHome, e.contextWork, e.contextOther);
    if (ctx) entry.contexts = ctx;
    if (e.pref) entry.pref = 1;
    if (e.label.trim()) entry.label = e.label.trim();
    map[e.key] = entry;
  }
  return map;
}

function buildPhoneMap(phones: PhoneVM[]): Record<string, unknown> | null {
  const valid = phones.filter((p) => p.number.trim());
  if (valid.length === 0) return null;
  const map: Record<string, unknown> = {};
  for (const p of valid) {
    const entry: Record<string, unknown> = { number: p.number.trim() };
    const ctx = buildContexts(p.contextHome, p.contextWork, p.contextOther);
    if (ctx) entry.contexts = ctx;
    const features: Record<string, boolean> = {};
    if (p.featureVoice) features.voice = true;
    if (p.featureCell) features.cell = true;
    if (p.featureFax) features.fax = true;
    if (p.featureText) features.text = true;
    if (p.featureVideo) features.video = true;
    if (Object.keys(features).length > 0) entry.features = features;
    if (p.pref) entry.pref = 1;
    if (p.label.trim()) entry.label = p.label.trim();
    map[p.key] = entry;
  }
  return map;
}

function buildAddressMap(addresses: AddressVM[]): Record<string, unknown> | null {
  const valid = addresses.filter(
    (a) => a.street.trim() || a.locality.trim() || a.full.trim(),
  );
  if (valid.length === 0) return null;
  const map: Record<string, unknown> = {};
  for (const a of valid) {
    const entry: Record<string, unknown> = {};
    const components: JsAddressComponent[] = [];
    if (a.street.trim()) components.push({ kind: 'name', value: a.street.trim() });
    if (a.locality.trim()) components.push({ kind: 'locality', value: a.locality.trim() });
    if (a.region.trim()) components.push({ kind: 'region', value: a.region.trim() });
    if (a.postcode.trim()) components.push({ kind: 'postcode', value: a.postcode.trim() });
    if (a.country.trim()) components.push({ kind: 'country', value: a.country.trim() });
    // Preserve extra components (round-trip unknown kinds).
    for (const ec of a.extraComponents) {
      if (ec.value.trim()) components.push(ec);
    }
    if (components.length > 0) entry.components = components;
    if (a.full.trim()) entry.full = a.full.trim();
    const ctx = buildContexts(a.contextHome, a.contextWork, a.contextOther);
    if (ctx) entry.contexts = ctx;
    if (a.pref) entry.pref = 1;
    if (a.label.trim()) entry.label = a.label.trim();
    if (a.countryCode.trim()) entry.countryCode = a.countryCode.trim();
    map[a.key] = entry;
  }
  return map;
}

function buildOrgMap(orgs: OrgVM[]): Record<string, unknown> | null {
  const valid = orgs.filter((o) => o.name.trim());
  if (valid.length === 0) return null;
  const map: Record<string, unknown> = {};
  for (const o of valid) {
    const entry: Record<string, unknown> = { name: o.name.trim() };
    const units = o.units.filter((u) => u.trim());
    if (units.length > 0) entry.units = units;
    map[o.key] = entry;
  }
  return map;
}

function buildTitleMap(titles: TitleVM[]): Record<string, unknown> | null {
  const valid = titles.filter((t) => t.name.trim());
  if (valid.length === 0) return null;
  const map: Record<string, unknown> = {};
  for (const t of valid) {
    const entry: Record<string, unknown> = { name: t.name.trim() };
    if (t.kind.trim()) entry.kind = t.kind.trim();
    if (t.orgId.trim()) entry.organizationId = t.orgId.trim();
    map[t.key] = entry;
  }
  return map;
}

function buildNicknameMap(nicknames: NicknameVM[]): Record<string, unknown> | null {
  const valid = nicknames.filter((n) => n.name.trim());
  if (valid.length === 0) return null;
  const map: Record<string, unknown> = {};
  for (const n of valid) {
    map[n.key] = { name: n.name.trim() };
  }
  return map;
}

function buildLinkMap(links: LinkVM[]): Record<string, unknown> | null {
  const valid = links.filter((l) => l.uri.trim());
  if (valid.length === 0) return null;
  const map: Record<string, unknown> = {};
  for (const l of valid) {
    const entry: Record<string, unknown> = { uri: l.uri.trim() };
    if (l.kind.trim()) entry.kind = l.kind.trim();
    if (l.label.trim()) entry.label = l.label.trim();
    map[l.key] = entry;
  }
  return map;
}

function buildNoteMap(notes: NoteVM[]): Record<string, unknown> | null {
  const valid = notes.filter((n) => n.note.trim());
  if (valid.length === 0) return null;
  const map: Record<string, unknown> = {};
  for (const n of valid) {
    map[n.key] = { note: n.note.trim() };
  }
  return map;
}

function buildAnniversaryMap(anniversaries: AnniversaryVM[]): Record<string, unknown> | null {
  const valid = anniversaries.filter((a) => a.month || a.day || a.year);
  if (valid.length === 0) return null;
  const map: Record<string, unknown> = {};
  for (const a of valid) {
    const date: Record<string, unknown> = { '@type': 'PartialDate' };
    const year = parseInt(a.year, 10);
    const month = parseInt(a.month, 10);
    const day = parseInt(a.day, 10);
    if (!isNaN(year) && a.year) date.year = year;
    if (!isNaN(month) && a.month) date.month = month;
    if (!isNaN(day) && a.day) date.day = day;
    map[a.key] = { kind: a.kind || 'birth', date };
  }
  return map;
}

// ── Photo helpers (REQ-CONT-60..63) ──────────────────────────────────────────

/** MIME types accepted for contact photos (server-enforced; reject client-side first). */
export const SUPPORTED_PHOTO_TYPES = [
  'image/jpeg',
  'image/png',
  'image/gif',
  'image/webp',
  'image/avif',
] as const;

/** Maximum photo blob size the server enforces (10 MiB). */
export const MAX_PHOTO_SIZE = 10 * 1024 * 1024;

/**
 * Validate a photo file before upload (REQ-CONT-61).
 * Returns an error message string when the file is invalid, or null when valid.
 */
export function validatePhotoFile(file: File): string | null {
  if (file.size > MAX_PHOTO_SIZE) {
    const mb = (file.size / 1024 / 1024).toFixed(1);
    return `Photo must be under 10 MiB (this file is ${mb} MiB).`;
  }
  if (!(SUPPORTED_PHOTO_TYPES as readonly string[]).includes(file.type)) {
    return `Unsupported image type: ${file.type || '(unknown)'}. Supported: JPEG, PNG, GIF, WebP, AVIF.`;
  }
  return null;
}

/**
 * Extract the current photo from a raw JSContact Card.
 * Returns { blobId, mediaType } or null when the Card has no photo.
 */
export function extractPhotoFromCard(
  raw: Record<string, unknown>,
): { blobId: string; mediaType: string } | null {
  const media = raw.media as Record<string, unknown> | undefined;
  if (!media) return null;
  for (const v of Object.values(media)) {
    if (typeof v === 'object' && v !== null) {
      const obj = v as Record<string, unknown>;
      if (obj.kind === 'photo' && typeof obj.blobId === 'string') {
        return {
          blobId: obj.blobId,
          mediaType: typeof obj.mediaType === 'string' ? obj.mediaType : 'image/jpeg',
        };
      }
    }
  }
  return null;
}

/**
 * Build a media map that includes a photo entry for the given blobId.
 * Non-photo entries from the original media map are preserved.
 * The photo entry uses the key "photo1".
 */
export function buildMediaWithPhoto(
  originalMedia: Record<string, unknown> | undefined,
  blobId: string,
  mediaType: string,
): Record<string, unknown> {
  const result: Record<string, unknown> = {};
  if (originalMedia) {
    for (const [key, v] of Object.entries(originalMedia)) {
      if (typeof v === 'object' && v !== null) {
        const obj = v as Record<string, unknown>;
        if (obj.kind === 'photo') continue; // remove old photo entry
      }
      result[key] = v;
    }
  }
  result['photo1'] = { '@type': 'MediaResource', kind: 'photo', blobId, mediaType };
  return result;
}

/**
 * Build a media map with all photo entries removed.
 * Returns null when no non-photo entries remain (JSON Merge Patch: delete the key).
 */
export function buildMediaWithPhotoRemoved(
  originalMedia: Record<string, unknown> | undefined,
): Record<string, unknown> | null {
  if (!originalMedia) return null;
  const result: Record<string, unknown> = {};
  for (const [key, v] of Object.entries(originalMedia)) {
    if (typeof v === 'object' && v !== null) {
      const obj = v as Record<string, unknown>;
      if (obj.kind === 'photo') continue;
    }
    result[key] = v;
  }
  return Object.keys(result).length > 0 ? result : null;
}

// ── Validators ────────────────────────────────────────────────────────────────

/** Minimal RFC 5322 email syntax check. */
function isValidEmail(addr: string): boolean {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(addr);
}

// ── Test surface ──────────────────────────────────────────────────────────────

export const _internals_forTest = {
  cardToVM,
  vmToCard,
  vmToPatch,
  validateVM,
  buildName,
  formatAddress,
  isValidEmail,
  validatePhotoFile,
  extractPhotoFromCard,
  buildMediaWithPhoto,
  buildMediaWithPhotoRemoved,
  splitDisplayName,
};
