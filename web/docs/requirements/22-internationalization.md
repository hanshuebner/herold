# 22 — Internationalization

Tabard ships with translations and locale-aware formatting from v1, replacing the prior English-only stance.

## Locales

| Locale | Region | Notes |
|--------|--------|-------|
| `en-US` | United States | Source language for all UI strings; default fallback. |
| `en-GB` | United Kingdom | Diverges from `en-US` for date format (DD/MM/YYYY vs MM/DD/YYYY) and a small set of spellings (organize → organise). |
| `de-DE` | Germany | Primary target after en. |
| `de-AT` | Austria | Diverges from `de-DE` only where vocabulary materially differs (`Jänner` vs `Januar`, etc.). |
| `de-CH` | Switzerland | German-Swiss conventions, no ß (always ss). |
| `fr-FR` | France | |
| `fr-BE` | Belgium | French-Belgian conventions (numerals, time formatting). |
| `fr-CA` | Canada | French-Canadian conventions; some vocabulary divergence (`courriel` vs `e-mail`). |
| `fr-CH` | Switzerland | French-Swiss conventions. |

Other locales (Italian, Spanish, Dutch, Nordic languages, Polish, Czech, Japanese, Chinese, etc.) are out for v1; revisit when there's user demand.

Right-to-left scripts (Arabic, Hebrew) are out for v1 — all target locales are LTR.

## Requirements

### Language scope

| ID | Requirement |
|----|-------------|
| REQ-I18N-01 | All user-visible UI strings are externalised into locale resource bundles. No hardcoded English in the source outside of source-language defaults. |
| REQ-I18N-02 | Locale resource format: ICU MessageFormat (`{count, plural, one {1 message} other {# messages}}`), one JSON file per locale per app package. ICU handles plurals, gender, ordinals — no hand-rolled "{count} message{s}" patterns. |
| REQ-I18N-03 | Source language is `en-US`. Every UI string has an `en-US` entry; other locales fall back to `en-US` for missing keys. |
| REQ-I18N-04 | Translation completeness target: `de-DE` and `fr-FR` 100% before v1 ship; regional variants (`-AT`, `-CH`, `-BE`, `-CA`) at least the entries that differ materially from their parent (the rest fall back via the locale chain `de-AT → de-DE → en-US`, etc.). |

### Locale selection

| ID | Requirement |
|----|-------------|
| REQ-I18N-10 | The active locale is decided in priority order: (1) explicit user choice in settings; (2) the user's `Accept-Language` header sent by the browser, mapped to the closest supported locale; (3) `en-US` fallback. |
| REQ-I18N-11 | The user's chosen locale persists in `localStorage` per account. A logged-out reload uses Accept-Language until login. |
| REQ-I18N-12 | Locale switching is live: changing the locale in settings re-renders the UI without reload. |
| REQ-I18N-13 | The active locale is also sent to herold via the JMAP session (or a custom property on the account) so server-generated text — vacation responder default, system messages in chat ("Charlotte left the Space"), bounce DSN content — is localised. Tabard surfaces herold's localised output verbatim; if a particular string isn't yet localised on the herold side, the en-US fallback is shown. |

### Date / time / number formatting

| ID | Requirement |
|----|-------------|
| REQ-I18N-20 | All date / time / number / currency formatting uses the browser's `Intl` APIs with the active locale. No hand-rolled formatters. |
| REQ-I18N-21 | Relative dates ("yesterday", "last Tuesday", "20. Apr.") use `Intl.RelativeTimeFormat` and the locale's typical conventions. The "in this year" / "older year shown" cutoff (REQ-UI-10e) is tabard logic; the *rendering* of the resulting date is locale-aware. |
| REQ-I18N-22 | Time-of-day uses 12-hour or 24-hour format per locale convention (en-US: 12-hour by default; de-*, fr-*: 24-hour). User can override in settings. |
| REQ-I18N-23 | Numerals in counts (e.g. unread count, attendee count) use the locale's grouping separator: `1,247` in en, `1.247` in de, `1 247` in fr. |

### Keyboard shortcuts and locale

| ID | Requirement |
|----|-------------|
| REQ-I18N-30 | Keyboard shortcuts (`10-keyboard.md`) are NOT locale-dependent. The default bindings are the same in every locale; the user can rebind in settings (REQ-KEY-04). German users still press `c` for compose, not `s` (Schreiben) — muscle memory across Gmail-compatible clients matters more than first-letter hint. |
| REQ-I18N-31 | The shortcut help overlay (`?`) labels each binding with the locale's translated action name, but the binding itself (the key) is unchanged. |

### Pluralization and grammar

| ID | Requirement |
|----|-------------|
| REQ-I18N-40 | All count-bearing strings ("3 messages", "1 unread") use ICU plural rules. Languages with multiple plural forms (Polish, Russian — out for v1, but for future) have their forms accommodated via the same mechanism. |
| REQ-I18N-41 | Grammatical gender — when a string varies by the gender of an addressee or subject (e.g. some Romance-language phrasings) — uses ICU `select` syntax. We do not require gender to be set; defaults are gender-neutral when possible. |

### Translation contributions

| ID | Requirement |
|----|-------------|
| REQ-I18N-50 | Locale resource bundles are JSON files in source control; contributions land via the same PR flow as code. There is no third-party translation platform in v1 (Crowdin, Lokalise, Weblate). |
| REQ-I18N-51 | A documented translator-onboarding doc lives in `apps/<app>/docs/translating.md` (when monorepo splits) or `docs/notes/translating.md` until then. Covers: where to find strings, ICU MessageFormat basics, locale-specific gotchas. |
| REQ-I18N-52 | A "missing translations" debug overlay (developer-only, behind a query parameter `?i18n-debug=missing`) highlights any string that fell back to a non-active locale. Used to spot gaps before release. |

### Accessibility

| ID | Requirement |
|----|-------------|
| REQ-I18N-60 | Each locale's resource bundle includes the appropriate `<html lang>` value; tabard sets it dynamically on locale change so screen readers pronounce content correctly. |
| REQ-I18N-61 | Locale-specific quotation marks („Beispiel" in de; «exemple» in fr; "example" in en) are produced by ICU MessageFormat or by the source content, not hand-rolled in templates. |

## Out of scope

- RTL scripts (Arabic, Hebrew) — none of our v1 target languages are RTL. Adding RTL is a layout-time effort (logical CSS properties, mirrored components) plus translation; deferred.
- Asian locales (CJK) — character rendering, line-breaking, sort order all need attention; deferred until a user need surfaces.
- A translation memory or terminology base — too much process for a single-author project; revisit when a translation team forms.
- Per-Identity locale (different language for different from-addresses). Single account, single user (NG3); one locale per user.
- Right-to-left for chat — same answer.
