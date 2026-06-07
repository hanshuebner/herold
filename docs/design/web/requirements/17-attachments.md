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

## Large or unsafe attachments: offload to a share link

Server contract: `../../server/requirements/25-attachment-shares.md` (`REQ-SHARE-*`). When the `https://netzhansa.com/jmap/file-shares` capability is absent from the session descriptor, none of REQ-ATT-60..73 apply and the affordance is hidden; compose behaves exactly as REQ-ATT-01..08.

| ID | Requirement |
|----|-------------|
| REQ-ATT-60 | An added attachment is a candidate for **offload** — uploading it to herold storage and replacing it in the message with a download link — when either (a) its size is at or above the offload threshold (`server.attachment_shares` surfaced via the file-shares capability; suite default 25 MB), or (b) its filename matches the unsafe-type list (the REQ-ATT-30 extensions plus archive types `.zip .rar .7z .tar .gz .tgz .dmg .iso .img`). Inline images are never offload candidates. |
| REQ-ATT-61 | **Offer below the hard limit, force above it.** When a candidate would still fit under the message's hard submission size limit (`maxSizeUpload`), the suite offers a choice via a confirm dialog ("`name` is `size`. Send it as a download link instead of attaching?" — "Upload as link" / "Attach anyway"). When the candidate is at or above the hard limit, plain attachment is disabled and offload is the only way to include the file; the dialog drops the "Attach anyway" option and explains why. An unsafe-type candidate under the size limit defaults the dialog to "Upload as link" but keeps "Attach anyway" enabled. |
| REQ-ATT-62 | Offload reuses the already-uploaded blob (REQ-ATT-02) — it does NOT re-upload. On the user choosing "Upload as link", the suite issues `FileShare/set create { blobId, name, type, expiresIn, maxDownloads?, password? }` against the blob already obtained for that file, then removes the file from the MIME attachment set. There is no second upload and no progress bar beyond the original. |
| REQ-ATT-63 | On a successful `FileShare/set create`, the suite inserts the share into the message body **in both alternatives**: a styled link block into the `text/html` body (filename, human size, type icon, expiry date, and the `url` as the anchor) and a plain-text line (`name (size) — url`) into the `text/plain` alternative. The block is inserted at the cursor, or appended above the signature when compose is not focused in the body. The inserted link is ordinary body content — it carries no special markup and survives autosave like any other edit (REQ-DFT-03). |
| REQ-ATT-64 | Offloaded files appear in a dedicated **"Shared links"** strip, visually distinct from the attachment chip strip, each row showing: filename, size, type icon, expiry ("expires in 30 days"), an optional download-cap badge, a lock icon when password-protected, and a remove control. Removing a row issues `FileShare/set destroy` and deletes the corresponding link block from both body alternatives. |
| REQ-ATT-65 | Before creating the share the suite MAY offer, behind a small "Options" disclosure on the offer dialog, an expiry selector (presets within `max_ttl`; default `default_ttl`), an optional download cap, and an optional password. When a password is set, the suite shows the password once with a copy affordance and a reminder that it must be shared out-of-band; the password is write-only and cannot be retrieved later (REQ-SHARE-43). |
| REQ-ATT-66 | Shares are created in the `pending` state (REQ-SHARE-20). On a successful send (after `EmailSubmission/set`), the suite batches `FileShare/set update { <id>: { state: "active" } }` for every share whose link is in the message. If the user discards the compose, the suite issues `FileShare/set destroy` for every `pending` share it created; any it misses expire server-side within `pending_ttl` and never become durable orphans. |
| REQ-ATT-67 | If `FileShare/set create` fails (over quota, too many shares, network), the dialog surfaces the server error ("You're out of share storage — N MB over") and the file stays a normal attachment (or, when offload was forced by REQ-ATT-61, the file cannot be included and the chip shows the blocking error). The suite never silently drops the file. |
| REQ-ATT-68 | Replying to or forwarding a message whose body contains share links treats those links as ordinary body text — the suite does not re-offload, re-host, or rewrite them. A forwarded share link points at the original sender's share and breaks when that share expires; this is expected and not the suite's concern. |

### Managing your shares

| ID | Requirement |
|----|-------------|
| REQ-ATT-70 | A "Shared files" view (reachable from Settings, `20-settings.md`) lists the principal's shares via `FileShare/query` + `FileShare/get`, showing filename, size, created date, expiry, download count (and cap, if any), lock state, and state (`active` / `revoked`). Sortable by created date and expiry. |
| REQ-ATT-71 | Each active share offers **Revoke** (`FileShare/set update` → revoked, or `destroy`), **Copy link**, and shortening expiry. Revoking takes effect immediately server-side (REQ-SHARE-22); the suite warns that any already-sent message linking the share will show a dead link. |
| REQ-ATT-72 | The view surfaces share-quota usage ("3.1 GB of 5 GB used") from the file-shares capability so the user understands why a create might be refused (REQ-ATT-67). |
| REQ-ATT-73 | Download counts are not live: the view reflects `download_count` as of its last `FileShare/get` and refreshes on open / manual refresh (REQ-SHARE-44). The suite does not promise real-time download notifications. |

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

- Third-party Drive/Dropbox-style cloud-attachment links (`Send a Drive link instead of the file`). Integrating an external cloud provider is out (`../00-scope.md`). herold's own storage-backed share links are in scope and specified above (REQ-ATT-60..73, `../../server/requirements/25-attachment-shares.md`) — this exclusion is only about third-party Drive integration.
- Server-side virus scanning. That's herold's job; the suite surfaces the result if herold sets a per-attachment flag (TBD on the server contract — file in `../notes/server-contract.md` if/when herold ships this).
- Encrypted attachments via PGP/MIME, S/MIME. NG5.
- Editing an attachment in-place (rich-text or office formats). Out forever.
