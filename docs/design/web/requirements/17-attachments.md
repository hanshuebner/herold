# 17 — Attachments

How files attach to outgoing messages, how received attachments render and download, and what the suite does about hostile attachments.

## Outbound: attaching files

| ID | Requirement |
|----|-------------|
| REQ-ATT-01 | Compose accepts attachments and inline images via four input paths: (a) file picker (toolbar "Attach file" button), (b) paste of a clipboard image while the cursor is in the body, (c) drag-and-drop onto an explicit **inline drop target** rendered inside the compose body region, (d) drag-and-drop onto an explicit **attachment drop target** rendered alongside the attachment chip strip. The two drop targets are visibly distinct, labelled ("Drop image here to inline" vs "Drop file here to attach"), and only become highlighted while a drag is active. The compose window MUST NOT auto-route a dropped file by guessing from MIME type — the user picks the target. The file picker always attaches; clipboard paste in the body always inlines. |
| REQ-ATT-02 | Each attachment or inline image is uploaded immediately via `Blob/upload` (RFC 8620 §6.1) when added to the compose, returning a `blobId`. The blobId persists with the draft. |
| REQ-ATT-03 | While upload is in flight, the attachment or inline-image chip shows a progress indicator. On failure, the chip turns red and shows "Upload failed — Retry" / "Remove". |
| REQ-ATT-04 | The total size of uploaded blobs (attachments + inline images) is checked against `urn:ietf:params:jmap:core/maxSizeUpload` from the session descriptor before each upload. Over-quota uploads are refused with a clear "this would exceed your quota by N MB" message. |
| REQ-ATT-05 | Attachment chips show: file name, size (human-readable), type icon, remove button. Inline-image chips appear in the body where the image is placed (image renders directly in flow); a small overlay reveals "Move to attachments" + "Remove" on hover or focus. |
| REQ-ATT-06 | Send composes the final `Email` with `attachments` referencing the uploaded blobIds, plus `bodyValues` and `bodyStructure` per RFC 8621 §4.1.4. Inline images carry `disposition: "inline"` + a Content-ID; attachments carry `disposition: "attachment"`. The classification follows the user's drop-target choice, not heuristics. |
| REQ-ATT-07 | Inline images and attachments are mutually convertible at any point during compose: dragging an inline image out of the body onto the attachment drop target moves it to attachments (`disposition` flips, the inline reference disappears from the body). Dragging an attachment onto the inline drop target moves it back into the body at the cursor position with a fresh Content-ID. State changes auto-save like any other field (REQ-DFT-03). |
| REQ-ATT-08 | If the user closes the compose without sending, uploaded blobs remain referenced by the draft. They are not orphaned: discarding the draft via REQ-DFT-XX (`19-drafts.md`) destroys the blob references along with the Email. |

## Inbound: rendering attachments

| ID | Requirement |
|----|-------------|
| REQ-ATT-20 | The reading pane lists each `Email.attachments` entry as a chip below the body, showing: file name, size, type icon. |
| REQ-ATT-21 | Image attachments (`type: image/*`) get a thumbnail preview. Thumbnails are loaded via the image proxy (`../notes/server-contract.md` § Image proxy), respecting the "external images blocked by default" rule (`13-nonfunctional.md` REQ-SEC-05). |
| REQ-ATT-22 | Inline (CID-referenced) images embedded in HTML body content are blocked by default and become loadable via the same "Load images" affordance that controls external images. Once loaded, they render in flow at the position the sender placed them. |
| REQ-ATT-23 | PDF attachments get an "Open" link that fetches the blob and opens it in a new tab via `<a target="_blank" rel="noopener noreferrer">`. No in-page PDF render in v1 (out of scope; PDF.js is a 1 MB dependency). |
| REQ-ATT-24 | All other attachment types: "Download" link only. |
| REQ-ATT-25 | The suite fetches attachment and inline-image content via `Blob/get` or `GET /jmap/download/<account-id>/<blob-id>/<filename>` per RFC 8620 §6.2. Authentication header is the bearer token. |
| REQ-ATT-26 | Each rendered inline image is independently downloadable with a single action: a small download button overlays the image on hover and on keyboard focus, the right-click context menu offers "Save image as…", and the image is also listed (with its filename and size) in an "Inline images" sub-section of the attachment chip strip below the body, with the same chip-level download button as attachments. The download path uses the blobId per REQ-ATT-25. (G16.) |

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
| REQ-ATT-40 | "Download all" zips every attachment AND every inline image in the thread and downloads as `<thread-subject>.zip`. Implementation: client-side zip via a small library (e.g. fflate). Inline images appear in the zip under their original filename (or `inline-<n>.<ext>` when the message has none), in a top-level `inline/` subfolder so the user can tell them apart from attachments. |
| REQ-ATT-41 | The "Download all" affordance shows the combined count: "Download all (5)" where 5 = attachments + inline images. A secondary "Attachments only" option excludes inline images for users who want the prior behaviour. (Reverses the rev-9 default per G16.) |

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
