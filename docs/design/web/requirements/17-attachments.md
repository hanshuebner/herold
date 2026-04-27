# 17 — Attachments

How files attach to outgoing messages, how received attachments render and download, and what the suite does about hostile attachments.

## Outbound: attaching files

| ID | Requirement |
|----|-------------|
| REQ-ATT-01 | Compose accepts attachments via: file picker (toolbar button), drag-and-drop onto the compose window, paste of a clipboard image. |
| REQ-ATT-02 | Each attachment is uploaded immediately via `Blob/upload` (RFC 8620 §6.1) when added to the compose, returning a `blobId`. The blobId persists with the draft. |
| REQ-ATT-03 | While upload is in flight, the attachment chip shows a progress indicator. On failure, the chip turns red and shows "Upload failed — Retry" / "Remove". |
| REQ-ATT-04 | The total size of attached blobs is checked against `urn:ietf:params:jmap:core/maxSizeUpload` from the session descriptor before each upload. Over-quota uploads are refused with a clear "this would exceed your quota by N MB" message. |
| REQ-ATT-05 | Attachment chips show: file name, size (human-readable), type icon, remove button. |
| REQ-ATT-06 | Send composes the final `Email` with `attachments` referencing the uploaded blobIds, plus `bodyValues` and `bodyStructure` per RFC 8621 §4.1.4. |
| REQ-ATT-07 | Pasted images become inline content via Content-ID (`cid:`) when the body is HTML; they appear inline in the body rather than as separate attachments below. The user can drag an inline image to "detach" (move to the attachment list). |
| REQ-ATT-08 | If the user closes the compose without sending, attached blobs remain referenced by the draft. They are not orphaned: discarding the draft via REQ-DFT-XX (`19-drafts.md`) destroys the blob references along with the Email. |

## Inbound: rendering attachments

| ID | Requirement |
|----|-------------|
| REQ-ATT-20 | The reading pane lists each `Email.attachments` entry as a chip below the body, showing: file name, size, type icon. |
| REQ-ATT-21 | Image attachments (`type: image/*`) get a thumbnail preview. Thumbnails are loaded via the image proxy (`../notes/server-contract.md` § Image proxy), respecting the "external images blocked by default" rule (`13-nonfunctional.md` REQ-SEC-05). |
| REQ-ATT-22 | Inline (CID-referenced) images embedded in HTML body content are blocked by default and become loadable via the same "Load images" affordance that controls external images. |
| REQ-ATT-23 | PDF attachments get an "Open" link that fetches the blob and opens it in a new tab via `<a target="_blank" rel="noopener noreferrer">`. No in-page PDF render in v1 (out of scope; PDF.js is a 1 MB dependency). |
| REQ-ATT-24 | All other attachment types: "Download" link only. |
| REQ-ATT-25 | The suite fetches attachment content via `Blob/get` or `GET /jmap/download/<account-id>/<blob-id>/<filename>` per RFC 8620 §6.2. Authentication header is the bearer token. |

## Suspicious attachments

| ID | Requirement |
|----|-------------|
| REQ-ATT-30 | Filenames ending in any of `.exe .bat .cmd .com .scr .pif .vbs .vbe .js .jse .ws .wsf .wsh .msi .msp .reg .lnk .scf .ps1 .ps1xml .ps2 .ps2xml .psc1 .psc2 .jar .dll` get a warning chip and an "Open Download" button that requires explicit click — no single-action download. The warning text: "this file type can run programs on your computer". |
| REQ-ATT-31 | The warning is purely about the filename; we do NOT inspect content. (Mismatched extensions — a `.txt` that's actually an executable — would require sniffing, which is the operating system's job.) |
| REQ-ATT-32 | Macro-bearing office formats (`.docm`, `.xlsm`, `.pptm`, etc.) get a softer warning ("this file may contain macros that can run programs"); same explicit-click flow. |
| REQ-ATT-33 | The suite never auto-opens attachments. There is no "preview" path that runs untrusted content. |

## Bulk download

| ID | Requirement |
|----|-------------|
| REQ-ATT-40 | "Download all" zips every attachment in the thread and downloads as `<thread-subject>.zip`. Implementation: client-side zip via a small library (e.g. fflate). |
| REQ-ATT-41 | Bulk download does not include inline (CID) images — those rarely matter outside their HTML context. Surface as "Download all attachments (3)"; the count excludes inline. |

## Drafts and attachments

| ID | Requirement |
|----|-------------|
| REQ-ATT-50 | Each blob attached to a draft is referenced by blobId in the draft's `Email`. Auto-save (`19-drafts.md`) preserves the references. |
| REQ-ATT-51 | Resuming a draft restores the chips with their original metadata; the blobs are re-fetched on demand for thumbnail rendering. |
| REQ-ATT-52 | Send-failure-keeps-as-draft (`19-drafts.md`) preserves blob references across the failure. |

## Out of scope

- Drive-style cloud-attachment links (`Send a Drive link instead of the file`). This is a Drive integration; Drive is out (`../00-scope.md`).
- Server-side virus scanning. That's herold's job; the suite surfaces the result if herold sets a per-attachment flag (TBD on the server contract — file in `../notes/server-contract.md` if/when herold ships this).
- Encrypted attachments via PGP/MIME, S/MIME. NG5.
- Editing an attachment in-place (rich-text or office formats). Out forever.
