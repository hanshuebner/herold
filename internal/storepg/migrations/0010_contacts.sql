-- 0010_contacts.sql — Phase-2 Wave 2.6: JMAP for Contacts (REQ-PROTO-55,
-- RFC 9553 JSContact + the JMAP-Contacts binding draft). Mirrors
-- storesqlite 0010. Postgres idioms applied where helpful (BIGINT
-- IDENTITY, BOOLEAN, BYTEA, partial unique index); column shapes stay
-- isomorphic with SQLite so the migration tool copies row-by-row.
-- Forward-only.

CREATE TABLE address_books (
  id              BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  principal_id    BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  name            TEXT    NOT NULL,
  description     TEXT    NOT NULL DEFAULT '',
  color_hex       TEXT
    CHECK (color_hex IS NULL OR color_hex ~ '^#[0-9A-Fa-f]{6}$'),
  sort_order      INTEGER NOT NULL DEFAULT 0,
  is_subscribed   BOOLEAN NOT NULL DEFAULT TRUE,
  is_default      BOOLEAN NOT NULL DEFAULT FALSE,
  rights_mask     BIGINT  NOT NULL DEFAULT 0,
  created_at_us   BIGINT  NOT NULL,
  updated_at_us   BIGINT  NOT NULL,
  modseq          BIGINT  NOT NULL DEFAULT 0
);

CREATE INDEX idx_address_books_principal ON address_books(principal_id);
CREATE UNIQUE INDEX idx_address_books_default
  ON address_books(principal_id) WHERE is_default = TRUE;

CREATE TABLE contacts (
  id               BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  address_book_id  BIGINT  NOT NULL REFERENCES address_books(id) ON DELETE CASCADE,
  principal_id     BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  uid              TEXT    NOT NULL,
  jscontact_json   BYTEA   NOT NULL,
  display_name     TEXT    NOT NULL DEFAULT '',
  given_name       TEXT    NOT NULL DEFAULT '',
  surname          TEXT    NOT NULL DEFAULT '',
  org_name         TEXT    NOT NULL DEFAULT '',
  primary_email    TEXT    NOT NULL DEFAULT '',
  search_blob      TEXT    NOT NULL DEFAULT '',
  created_at_us    BIGINT  NOT NULL,
  updated_at_us    BIGINT  NOT NULL,
  modseq           BIGINT  NOT NULL DEFAULT 0
);

CREATE INDEX idx_contacts_book                ON contacts(address_book_id);
CREATE INDEX idx_contacts_principal           ON contacts(principal_id);
CREATE INDEX idx_contacts_modseq              ON contacts(address_book_id, modseq);
CREATE UNIQUE INDEX idx_contacts_uid          ON contacts(address_book_id, uid);
CREATE INDEX idx_contacts_display_name        ON contacts(principal_id, display_name);
CREATE INDEX idx_contacts_primary_email       ON contacts(principal_id, primary_email);

ALTER TABLE jmap_states ADD COLUMN address_book_state BIGINT NOT NULL DEFAULT 0;
ALTER TABLE jmap_states ADD COLUMN contact_state      BIGINT NOT NULL DEFAULT 0;
