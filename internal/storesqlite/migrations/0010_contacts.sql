-- 0010_contacts.sql — Phase-2 Wave 2.6: JMAP for Contacts (REQ-PROTO-55,
-- RFC 9553 JSContact + the JMAP-Contacts binding draft).
--
-- Two new tables (address_books, contacts) plus two additive jmap_states
-- columns (address_book_state, contact_state). Mirrors the storepg 0010
-- migration of the same name; column shapes stay isomorphic so the
-- backup / migration tooling moves rows row-for-row across backends.
--
-- Storage strategy for the JSContact object: we persist the full RFC 9553
-- object verbatim as JSContact JSON in contacts.jscontact_json. A handful
-- of denormalised columns (display_name, given_name, surname, org_name,
-- primary_email, search_blob) carry the values JMAP queries filter and
-- sort on so the read paths do not have to JSON-parse every row. The
-- protojmap serializer (parallel agent) is the sole producer of those
-- columns: it parses the JSContact, derives the denormalised fields, and
-- hands the store a fully-populated Contact. Future RFC 9553 additions
-- land in the JSON blob without a schema change; only when a new field
-- needs to be filterable does it earn a column.
--
-- IsDefault enforcement: a partial unique index pins at most one
-- is_default=1 row per principal. The Go layer flips the previous
-- default to 0 inside the same tx when a caller marks a different
-- address book as default; the index is the safety net.
--
-- Forward-only.

CREATE TABLE address_books (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  principal_id    INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  name            TEXT    NOT NULL,
  description     TEXT    NOT NULL DEFAULT '',
  color_hex       TEXT,
  sort_order      INTEGER NOT NULL DEFAULT 0,
  is_subscribed   INTEGER NOT NULL DEFAULT 1,
  is_default      INTEGER NOT NULL DEFAULT 0,
  rights_mask     INTEGER NOT NULL DEFAULT 0,
  created_at_us   INTEGER NOT NULL,
  updated_at_us   INTEGER NOT NULL,
  modseq          INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX idx_address_books_principal ON address_books(principal_id);
CREATE UNIQUE INDEX idx_address_books_default
  ON address_books(principal_id) WHERE is_default = 1;

CREATE TABLE contacts (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  address_book_id  INTEGER NOT NULL REFERENCES address_books(id) ON DELETE CASCADE,
  principal_id     INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  uid              TEXT    NOT NULL,
  jscontact_json   BLOB    NOT NULL,
  display_name     TEXT    NOT NULL DEFAULT '',
  given_name       TEXT    NOT NULL DEFAULT '',
  surname          TEXT    NOT NULL DEFAULT '',
  org_name         TEXT    NOT NULL DEFAULT '',
  primary_email    TEXT    NOT NULL DEFAULT '',
  search_blob      TEXT    NOT NULL DEFAULT '',
  created_at_us    INTEGER NOT NULL,
  updated_at_us    INTEGER NOT NULL,
  modseq           INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX idx_contacts_book                ON contacts(address_book_id);
CREATE INDEX idx_contacts_principal           ON contacts(principal_id);
CREATE INDEX idx_contacts_modseq              ON contacts(address_book_id, modseq);
CREATE UNIQUE INDEX idx_contacts_uid          ON contacts(address_book_id, uid);
CREATE INDEX idx_contacts_display_name        ON contacts(principal_id, display_name);
CREATE INDEX idx_contacts_primary_email       ON contacts(principal_id, primary_email);

ALTER TABLE jmap_states ADD COLUMN address_book_state INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jmap_states ADD COLUMN contact_state      INTEGER NOT NULL DEFAULT 0;
