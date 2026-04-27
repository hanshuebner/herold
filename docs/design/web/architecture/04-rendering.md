# 04 — Rendering

How HTML message bodies are displayed safely.

## The problem

Email HTML is hostile by default: tracking pixels, `<script>` payloads, CSS that escapes the message frame, links that don't say where they go. The client cannot trust any of it.

## Approach: sandboxed iframe per message

Each message body renders inside its own `<iframe>` with:

```
<iframe sandbox="allow-same-origin"
        srcdoc="<!doctype html>...sanitised body..."
        referrerpolicy="no-referrer"
        loading="lazy">
```

- `sandbox` attribute is set with **only** `allow-same-origin` — no `allow-scripts`, no `allow-forms`, no `allow-popups`, no `allow-top-navigation`. Scripts in the message body cannot execute.
- `srcdoc` carries the sanitised HTML directly; no separate URL to fetch and no chance for the browser to interpret a `<base>` redirection.
- `referrerpolicy="no-referrer"` prevents the iframe (in case any image does load) from leaking the user's URL to the sender.
- `loading="lazy"` defers rendering until the message is in or near the viewport.

## Sanitisation

Before serving as `srcdoc`:

- Drop `<script>`, `<style>` (we apply our own typography), `<iframe>`, `<object>`, `<embed>`, `<form>`, event-handler attributes (`on*`).
- Drop `javascript:` / `data:text/html` URLs in `href`/`src`.
- Rewrite `<a href>` to `target="_blank" rel="noopener noreferrer"`.
- Rewrite `<img src>` to point at the server-side image proxy (`../notes/server-contract.md` § Image proxy). Inline images (CID-referenced) remain blocked until the user clicks "Load images" per `../requirements/13-nonfunctional.md` REQ-SEC-05.
- Strip CSS @import, position:fixed, position:absolute that would escape the iframe (the iframe contains them anyway, but stripping is cheap insurance).

Sanitisation library: TBD (see `../implementation/01-tech-stack.md`). DOMPurify is the obvious choice; final pick depends on framework selection.

## Image loading

Default: blocked. Tabard renders a "Load images" pill at the top of the message. Clicking it:

1. Removes the block.
2. Optionally remembers the choice for this sender (per-`Identity` of the From header) — surfaced as "Always load images from foo@bar". Stored in `localStorage` keyed by the sender's email + identity.

The proxy origin is the same origin as the JMAP API. Tabard's `<meta http-equiv="Content-Security-Policy" content="img-src 'self'; ...">` enforces this — direct image loads from arbitrary origins are not possible.

## Plain-text bodies

Plain text doesn't go through the iframe path. Tabard wraps it in a `<pre>` with whitespace-normal wrapping plus a URL-linkifier: anything matching a URL regex becomes a `<a target="_blank" rel="noopener noreferrer">`. No `<style>`, no DOM injection.

## Compose body

The compose textarea is the user's own input; no sanitisation barrier needed. On send, tabard converts the user's HTML (if rich-text mode) into the body of the outgoing `Email`. Quoted history (for replies) is included as-is from the parent email's already-sanitised body, then re-sanitised on receive at the recipient's end.

## CSP

Tabard ships with a strict Content-Security-Policy:

```
default-src 'self';
script-src 'self';
style-src 'self' 'unsafe-inline';     // for the typography we apply
img-src 'self' data:;                  // 'self' covers the proxy origin
font-src 'self';
connect-src 'self';
frame-src 'self';                      // iframes use srcdoc, same-origin
form-action 'none';
base-uri 'none';
```

`'unsafe-inline'` for `style-src` is the one concession; it's necessary for any framework that mounts inline `<style>` for component-scoped CSS. The iframe's content is governed by its own CSP (set via the wrapping HTML in `srcdoc`).
