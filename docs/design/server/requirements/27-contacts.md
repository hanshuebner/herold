# 27 — Contacts (server extensions)

herold already implements the JMAP for Contacts datatype
(`urn:ietf:params:jmap:contacts`, RFC 9553 JSContact + the JMAP-Contacts
binding): `AddressBook/*`, `Contact/*` including `Contact/query`,
`Contact/changes`, and `Contact/set` (`01-protocols.md` REQ-PROTO-55). The
store keeps each contact as a full JSContact `Card` blob plus denormalized
columns (`display_name`, `given_name`, `surname`, `org_name`, `primary_email`,
`search_blob`) and a per-principal `contact_state` change counter
(`05-storage.md`).

This document specifies the server work the contacts app
(`../../web/requirements/27-contacts.md`) needs on top of that base: contact
photos wired to the blob store, a vCard 4.0 converter with an import/export
transport, and the guarantees the client relies on for atomic merge and
duplicate detection. Prefix `REQ-CTS-`.

## Contact photos

A contact photo is an image blob referenced from the `Card` via a JSContact
`media`/`photos` entry that carries a `blobId`. Today `Contact/set` round-trips
such entries opaquely; that leaves the referenced blob unvalidated and
un-collectable. These requirements make the photo a first-class, lifecycle-
managed reference.

| ID | Requirement |
|----|-------------|
| REQ-CTS-01 | On `Contact/set` (`create`/`update`), the server MUST recognise JSContact media entries whose value references a JMAP blob (a `blobId`, per the JMAP-Contacts binding's blob-reference form) and MUST validate that each referenced blob exists and is owned by the acting principal's account. An unknown or foreign `blobId` fails that contact with a `SetError` (`invalidProperties`), it does not create a dangling reference. |
| REQ-CTS-02 | Referenced photo blobs MUST be retained for the lifetime of the contact reference: a blob referenced by a live `Card` MUST NOT be garbage-collected. Blob GC MUST treat contact-photo references as roots alongside the existing blob-reference roots. |
| REQ-CTS-03 | When a contact is destroyed or its photo reference is removed/replaced, the previously referenced blob MUST become eligible for GC if no other reference holds it. Removal MUST NOT eagerly delete a blob that another contact (or datatype) still references. |
| REQ-CTS-04 | Photo blobs MUST be downloadable through the existing JMAP blob download path scoped to the account, so the client fetches a contact photo by its `blobId` with the same auth surface as any other blob. No separate contact-photo endpoint is introduced. |
| REQ-CTS-05 | The server MUST enforce a configurable maximum photo blob size and MUST reject non-image media types for photo references, returning a `SetError` rather than storing an unusable reference. |

## vCard 4.0 converter

The converter maps between RFC 6350 vCard 4.0 and RFC 9553 JSContact. It is a
wire-format parser and therefore carries the project's full parser obligations
(`../../STANDARDS.md` §8: fuzz target, deterministic tests, executable doc
examples).

| ID | Requirement |
|----|-------------|
| REQ-CTS-10 | The server MUST provide a vCard 4.0 -> JSContact parser that maps the standard properties the contacts app edits: `FN`/`N` -> `name`, `EMAIL` (+`TYPE`/`PREF`) -> `emails`, `TEL` (+`TYPE`/`PREF`) -> `phones`, `ADR` -> `addresses`, `ORG` -> `organizations`, `TITLE`/`ROLE` -> `titles`, `NICKNAME` -> `nicknames`, `URL` -> online services/links, `NOTE` -> `notes`, `BDAY`/`ANNIVERSARY` -> `anniversaries`, `PHOTO` -> a photo media entry, `KIND` -> `kind`, `UID` -> `uid`. `TYPE` parameters map to JSContact contexts/features. |
| REQ-CTS-11 | The parser MUST be total over arbitrary input: malformed, truncated, or non-UTF-8 vCard input yields a structured per-card error, never a panic. It MUST handle line folding, escaping, `CHARSET`, and multiple cards in one file. A fuzz target over the parser MUST exist and be part of the fuzz corpus. |
| REQ-CTS-12 | The server MUST provide a JSContact -> vCard 4.0 generator producing spec-conformant output. Properties the target vCard cannot represent MUST be reported to the caller (surfaced in the export summary, `../../web/requirements/27-contacts.md` REQ-CONT-83), not dropped silently. |
| REQ-CTS-13 | Round-trip: a JSContact `Card` generated to vCard and parsed back MUST yield an equivalent `Card` for the mappable property set. A conformance test MUST assert round-trip equivalence over a fixture set of representative cards. |
| REQ-CTS-14 | `PHOTO` handling on import MUST intern inline photo data (or fetch a referenced photo, subject to REQ-CTS-05 limits) into the blob store and emit a `blobId` reference (REQ-CTS-01); export MUST embed or reference the photo per the vCard the caller requested. |

## Import / export transport

| ID | Requirement |
|----|-------------|
| REQ-CTS-20 | The server MUST expose an import path that accepts an uploaded `.vcf` (one or many cards), parses each via REQ-CTS-10, creates the contacts in the acting principal's target address book, and returns a per-card result: created contact id, skipped (with reason), or failed (with the parser/`SetError` reason). Partial success is reported; a single bad card does not fail the batch. |
| REQ-CTS-21 | The import path MUST be idempotent-friendly for re-runs: it reports likely-duplicate candidates (by `uid`, then by shared email/phone) so the client can drive skip/create/merge (`../../web/requirements/27-contacts.md` REQ-CONT-82). The server does not auto-merge on import. |
| REQ-CTS-22 | The server MUST expose an export path that produces a `.vcf` for a single contact, a set of contact ids, or an entire address book, using the generator (REQ-CTS-12) and reporting unrepresentable-property warnings. |
| REQ-CTS-23 | Import and export MUST be authenticated and authorized to the acting principal and MUST enforce a bounded request size; a large import MUST stream/commit incrementally so a cancelled or interrupted import leaves already-created contacts persisted and consistent. |
| REQ-CTS-24 | The import/export transport MUST work identically on both storage backends (SQLite and Postgres) and MUST be covered by an end-to-end test that imports a fixture `.vcf`, verifies the created contacts via `Contact/get`, exports them, and asserts round-trip equivalence — on both backends. |

## Merge and duplicate-detection support

Merge and duplicate detection are client-driven
(`../../web/requirements/27-contacts.md` REQ-CONT-90..94); the server's
obligation is to make an atomic merge expressible and to make duplicate
candidates cheap to find.

| ID | Requirement |
|----|-------------|
| REQ-CTS-30 | A single `Contact/set` MUST apply its `create`/`update` and `destroy` in one atomic transaction, so a client merge — write the surviving/merged `Card`, destroy the other members in the same call — either fully succeeds or leaves every pre-merge contact intact. This is the existing JMAP `/set` atomicity guarantee; this requirement states the contacts app depends on it. |
| REQ-CTS-31 | `Contact/query` MUST support finding duplicate candidates without scanning the whole address book client-side: a `text` filter already matches an email or phone against `search_blob`, and the server MUST keep `primary_email` exact-match filtering available so the client can cheaply find contacts sharing an address. |
| REQ-CTS-32 | Destroying a group card (`kind: "group"`) MUST NOT cascade-delete member contacts (`../../web/requirements/27-contacts.md` REQ-CONT-72); membership references are resolved by the client. |

## Non-functional

| ID | Requirement |
|----|-------------|
| REQ-CTS-40 | All new code paths (photo wiring, vCard converter, import/export) MUST meet the project coverage standard, run on both backends in CI, and be deterministic (`../../STANDARDS.md` §8). The vCard parser MUST have a fuzz target (REQ-CTS-11). |
| REQ-CTS-41 | Import/export and photo interning MUST bound memory and request size so a large or hostile upload cannot exhaust the server; oversized inputs are rejected with a clear error. |
