# ADR-0003: Plugins as first-class extensions -- lifecycle, state, and UI

- Status: Proposed
- Date: 2026-07-13
- Area: server -- plugins, store, JMAP; web -- suite and admin settings
- Related requirements: REQ-PLUG-01..06 (lifecycle), REQ-PLUG-10..13 (config),
  REQ-PLUG-20..22 (manifest, options_schema), REQ-PLUG-30..34 (JSON-RPC wire),
  REQ-PLUG-40..44 (isolation), REQ-PLUG-50..53 (observability),
  REQ-PLUG-60..62 (distribution), REQ-PLUG-70..72 (ABI), REQ-ADM-* (admin REST),
  REQ-OPS-20..25 (appconfig), invariant 9 (system.toml is never mutated at runtime)
- Driver: ADR-0002 (generated spam filter) cannot be built as a plugin without all
  three capabilities below. The capabilities are general, so they are specified
  here rather than smuggled in as spam-specific special cases.

## Context

A herold plugin today is a server-side function and nothing more. It is spawned
from a `[[plugin]]` block in `system.toml`, it answers JSON-RPC on stdio, and it
is invisible to everything above the supervisor. Three gaps follow, and each one
independently blocks ADR-0002:

**There is no installation.** REQ-PLUG-60 says "no registry -- drop executables
into `<data_dir>/plugins/<name>/`", and REQ-PLUG-10 says plugins are declared in
system config. But invariant 9 forbids writing `system.toml` at runtime, so
enabling a plugin is a config-file edit plus a SIGHUP -- an operator-with-a-shell
operation. There is no enable, no disable, no removal, no state cleanup, and no
upgrade path. `GET /api/v1/server/status` reports `"plugins": []` as a hardcoded
stub (`internal/protoadmin/server_endpoints.go:199`).

**There is no plugin state.** REQ-PLUG-44 forbids a plugin touching the DB, and
nothing replaces it. A plugin that needs to remember anything per user -- a
ruleset, a trained model, a preference -- has nowhere to put it. `internal/store`
has no generic key-value surface; every feature gets a bespoke typed table.

**There is no plugin UI.** The suite's settings sections are a hardcoded string
union with static imports (`web/apps/suite/src/views/SettingsView.svelte:79-121`),
and the admin SPA has no server-persisted settings surface at all. A plugin cannot
present anything to a user, so a plugin cannot be *configured* by one.

The result is that plugins can only ever be things an operator configures in a
text file. Anything a *user* would tune has to be in core. That is the constraint
that pushed ADR-0002 toward an in-core scorer, and it is the wrong constraint to
design around.

## Decision

Three capabilities. They are independent in principle and interlocking in
practice, because a plugin you can install is useless if it cannot keep state, and
a plugin with state is useless if nobody can see it.

### 1. Lifecycle: discovery on disk, enablement in the DB

Binaries stay operator-placed. **There is no registry, no download, no
auto-update** -- that is a supply-chain surface herold has correctly refused, and
this ADR does not reopen it. Installing a plugin remains "put the executable in
`<data_dir>/plugins/<name>/`".

What changes is *registration*. Today it lives in `system.toml`; it moves to the
DB:

    discovered  executable present, manifest read, not running
    enabled     options set, supervised, serving requests
    disabled    options retained, not running
    failed      supervisor gave up (REQ-PLUG-05: >5 crashes in 5 min)
    removed     state purged

The server scans the plugin directory at startup and on demand, reads each
manifest, and records what it found. Enablement and options are admin-API state,
mutated through the store layer like every other piece of application config.

This is a change to REQ-PLUG-10, and it is the *point* of invariant 9 rather than
a violation of it: invariant 9 says infra-owned facts live in TOML and mutable
application config lives in the DB, and it already lists "spam policy" as DB
state. Which plugins are on and how they are tuned is application config. An
operator enabling a plugin from the admin SPA cannot write `system.toml`, and
should not.

Secrets keep their existing posture (REQ-PLUG-22: env or FIFO, never argv, never
the DB). An option declared `secret: true` stores a *reference* in the DB -- an
env var name or a `secrets/` entry -- and never the value.

**Removal purges state, and that has to be explicit.** A plugin that has been
accumulating per-principal rulesets for a year leaves orphans behind if `removed`
just means "stop running it". So removal is two steps: disable, then purge, with
an export offered first. Data ownership is the part of a plugin lifecycle that
specs habitually forget, and orphaned rows in a per-principal table are how a
mail server quietly grows a haunted schema.

**State survives upgrades or refuses to.** A plugin declares the state schema
versions it can read. On enable, a stored version the new binary does not
understand is a hard failure -- refuse to start, tell the operator -- rather than
a plugin silently reinterpreting bytes it does not understand.

### 2. State: server-mediated, per-plugin, optionally per-principal

REQ-PLUG-44 is right and stays. The plugin does not get the DB; it gets an RPC.
Three new plugin-to-server methods, extending REQ-PLUG-31's `log`/`metric`/`notify`:

    state.get(key, principal?)          -> value
    state.put(key, value, principal?)   -> ok
    state.list(prefix, principal?)      -> keys

Scoped to `(plugin_name, principal_id | NULL, key)`. A NULL principal is
plugin-global state. Values are opaque JSON to the server, quota-capped per plugin
and per principal, because a plugin must not be able to fill the operator's disk.

This wants one table with an untyped JSON value column, and that **is a deliberate
departure** from the schema's one-table-per-feature, STRICT-typed grain. The
justification is the whole premise of a plugin: core does not know the plugin's
schema and must not have to. The existing `jmap_categorisation_config` table
already reaches for a `category_set_json BLOB` when the shape is open-ended
(`internal/storesqlite/migrations/0009_categorisation.sql:15`); this generalises
that concession rather than inventing it.

### 3. UI: two tiers, one of them deferred

A plugin surface can be satisfied two ways, and v1 builds only the first:

    Tier 1  view tree   plugin returns JSON; the SPA renders it   -- built now
    Tier 2  component   plugin ships code; it runs sandboxed      -- deferred

Tier 2 is **deferred, not refused**. Rich plugin UI is a real requirement and it
will arrive; what it must not do is arrive by accident, through the one door that
would quietly destroy the plugin security model. Precisely one approach is
forbidden, and it is worth naming so nobody reaches for it later as the obvious
shortcut:

> **Plugin JavaScript must never execute in the suite's own origin.** The SPA's
> CSP is `script-src 'self'` (`internal/webspa/suite.go`, `buildCSP`), and the
> plugin security model (REQ-PLUG-40..44) exists to keep a plugin *less*
> privileged than the server: its own user, its own directory, no DB, no
> application config. Loading plugin JS as a module into the suite would hand it
> the session cookie and the whole mailbox -- strictly more access than the
> process isolation was built to deny it. Serving the asset from the herold origin
> to satisfy `script-src 'self'` is exactly what makes this tempting and exactly
> what makes it fatal.

Tier 2 therefore lands as a **sandboxed frame with an opaque origin**, which is
the same trick the suite already plays on untrusted mail HTML
(`web/apps/suite/src/lib/mail/sanitize.ts:563-579`, `sandbox="allow-same-origin"`
with no `allow-scripts`; the plugin case inverts that to `allow-scripts` with no
`allow-same-origin`). Assets are served same-origin from `/plugin-assets/<name>/`
so `script-src 'self'` is satisfied, but the frame carries no herold origin: no
cookie, no localStorage, no credentialed `/jmap` call. It reaches its own plugin,
and nothing else, through a postMessage bridge the parent brokers. The blast
radius is the plugin's own data.

That is a coherent design and it is not the design being built today, because it
needs a bridge protocol, a theming contract, and an accessibility story that tier
1 does not. What this ADR must do -- and the only thing it must do about tier 2
-- is make sure tier 1 does not wall the road:

- **Surfaces are the contract; tiers are an implementation of a surface.** The
  registry, the JMAP capability, and the suite's settings registry all key on
  `settings.panel` / `admin.panel` / `message.detail` -- never on "view tree". A
  tier-2 plugin fills the same surface through the same registration path.
- **The manifest declares its UI kind.** `"ui": { "kind": "view-tree" }` in v1,
  with `"kind": "component"` a reserved value. A plugin declaring the reserved
  kind against a server that cannot host it gets a clean "unsupported UI kind"
  refusal, not undefined behaviour.
- **The asset route is reserved now.** `/plugin-assets/` goes into
  `reservedAPIPrefixes` (`internal/webspa/suite.go:66-78`) in v1 even though it
  serves nothing, so the SPA's catch-all cannot squat it and adding tier 2 later
  is not a breaking change to routing.
- **Plugins declare permissions, and code is one of them.** The manifest grows a
  permission list whose only v1 value is the UI kind. Tier 2 needs an explicit
  operator grant -- "this plugin may run code in the browser" -- and that grant
  needs somewhere to live before it exists.

None of these cost anything in v1. All of them are expensive to retrofit.

A tier-1 plugin contributes a **view tree**, in JSON, from a closed component
vocabulary the SPA already implements:

```json
{
  "surface": "settings.panel",
  "title": "Spam",
  "nodes": [
    { "type": "textarea", "id": "policy", "label": "Filter policy",
      "value": "Mail from member domains is ham...", "rows": 12 },
    { "type": "chips", "id": "members", "label": "Member domains",
      "values": ["classic-computing.de", "vzekc.org"] },
    { "type": "button", "id": "compile", "label": "Apply", "style": "primary" },
    { "type": "table", "id": "rules", "columns": ["Rule", "Score", "Because"],
      "rows": [["member-domain-authenticated", "-6.0", "policy line 3: ..."]] }
  ]
}
```

Two JMAP methods carry it, under one capability -- `https://netzhansa.com/jmap/plugin-ui`:

    PluginUI/get    { plugin, surface }          -> view tree
    PluginUI/action { plugin, action, params }   -> view tree, plus optional toast

The server turns each into a `ui.render` / `ui.action` RPC on the plugin, with the
principal and the locale attached. The plugin owns the behaviour; it just does not
own the rendering. This is server-driven UI, and its properties are the ones tier 1
wants: nothing executes near the user's session, the payload is data the SPA
escapes on render, and there is deliberately no `html` node in the vocabulary --
an `html` node would be tier 2 without any of tier 2's isolation.

Component vocabulary, v1 -- enough to configure something and explain itself, and
nothing more. It is a floor, not a ceiling: the answer to "my plugin needs a chart"
is tier 2, not thirty more node types.

| Node | Purpose |
|---|---|
| `text`, `banner` | Prose, warnings |
| `textarea`, `input`, `number`, `toggle`, `select`, `chips` | Form fields |
| `button` | Dispatches an `action` |
| `table`, `keyvalue` | Structured display |
| `diff` | Before/after preview -- the shape a "what would this change?" flow needs |

Surfaces, v1:

| Surface | Where |
|---|---|
| `settings.panel` | A section in the suite's settings view, per principal |
| `admin.panel` | A section in the admin SPA, per operator |
| `message.detail` | A panel on an open message -- e.g. "why was this filed?" |

One capability, enumerated at runtime, rather than one capability per plugin. The
suite's frozen `Capability` union (`web/apps/suite/src/lib/jmap/types.ts:77-210`)
therefore needs exactly one new constant, and `SettingsView.svelte`'s hardcoded
`SECTIONS` array becomes a registry: first-party sections, then whatever
`PluginUI/get` reports.

Localisation: the render call carries the locale and the plugin returns strings
already localised. herold cannot translate text it has never seen, and a plugin
that ships only English is a plugin that displays English.

### 4. Data grants: the plugin asks, the operator consents, the server enforces

Today the shape of a plugin's payload is frozen in the ABI. `spam.classify` gets
`from`, `to`, `cc`, `subject`, `received_date`, three auth booleans, `from_domain`
and a body excerpt -- a compromise struck once, in REQ-FILT-30..32, on behalf of
every plugin that will ever exist. It is simultaneously too much (a cloud endpoint
gets the subject line and 4 KiB of body) and far too little (a local scorer cannot
see the Received chain it needs, and most of the signal in ADR-0002 is invisible
to it).

Both failures have one cause: **the payload is a constant where it should be a
contract.** So make it one.

A plugin **declares what it needs** in its manifest, element by element, each with
a reason the operator will actually read:

```toml
[[data]]
element  = "headers.all"
required = true
reason   = "Scoring reads the Received chain, Message-ID, Return-Path and List-* headers."

[[data]]
element  = "body.text"
required = false
limit    = "4KiB"
reason   = "Improves recall on image-only spam. The filter works without it."
```

The element vocabulary is closed, and it is ordered by how much it gives away:

| Element | What the plugin receives |
|---|---|
| `envelope` | MAIL FROM, RCPT TO |
| `auth` | SPF / DKIM / DMARC results |
| `headers.named` | Only the headers the plugin lists by name |
| `headers.all` | The complete header block |
| `body.text` | Plain-text body, to a declared limit |
| `body.html` | HTML body |
| `attachments.meta` | Filename, size, MIME type -- never content |

**There is deliberately no `message.raw`.** A "give me the whole message" element
would make every other element redundant and every transform below unenforceable,
which would reduce the grant from something the server enforces to something the
plugin is trusted about -- and a grant that is merely advisory is worse than no
grant, because it looks like one. A plugin that genuinely needs everything composes
`headers.all` + `body.text` + `body.html` + `attachments.meta`, and each of those
stays individually transformable and individually visible on the consent screen.
Attachment *content* has no element at all, and wants its own ADR before it gets
one.

Each granted element can carry an operator-chosen **transform**, which is where a
privacy posture stops being a slogan:

| Transform | Effect |
|---|---|
| `redact.addresses` | Addr-specs replaced by stable pseudonyms; domains preserved |
| `redact.names` | Display names stripped |
| `truncate:N` | Cap the bytes |

**At install, the operator confirms the shape of the data.** They see the table,
they toggle the optional elements, they pick transforms, and they accept. A
required element they decline means the plugin does not enable -- no silent
degradation into a filter that cannot see. The grant is stored with the plugin's
registration.

**The operator grants; every principal can see what was granted.** The consent
decision is the operator's -- they administer the server and can read the spool if
they choose, so a per-user veto would be ceremony rather than protection. But
"could read the spool" and "installed software that reads everyone's mail
continuously" are not the same fact, and the second one is a user's business. So
each principal gets a read-only view: which plugins can read their mail, which
elements each receives, which transforms apply, and whether the plugin sends
anything off the machine. Visibility without a veto, which is the honest shape of
the trust relationship on a single-operator server.

This gets harder to defend the moment a plugin's grant lets data leave the machine
on a server whose operator is not its only user, and a per-principal opt-in for
egressing plugins is the obvious next move if that day comes. It is not needed for
anything in flight, so it is not built.

The server then **builds the payload from the grant**. This is the load-bearing
half: it is a projection the server performs, not a promise the plugin makes. A
plugin cannot receive what it was not granted, however it is written and whatever
it asks for at call time.

An upgraded plugin whose manifest asks for *more* than the stored grant **does not
start**. It surfaces as "needs re-consent" and waits for a human. A plugin that can
silently widen its own access on upgrade is not a plugin with a data grant; it is
a plugin with a data grant-shaped decoration.

This reframes REQ-FILT-30..32 rather than deleting them: they stop being a law
binding every plugin forever and become **the default grant of the LLM classifier**
-- which is exactly the plugin whose payload leaves the building, and exactly the
plugin those rules were written for. The rationale REQ-FILT-31 gives ("LLM prompts
are data leakage surfaces") is a statement about an *egress*, and this mechanism
puts the control at the egress. A local scorer that never opens a socket gets the
full header block, and a cloud endpoint does not, and the difference is visible in
one screen instead of buried in an ABI.

It also unifies with the UI permission from tier 2: **one consent surface, listing
what a plugin may *see* and what it may *do*.** Those are the same question asked
twice, and an operator should answer them in one place.

Whether a transform costs accuracy is an empirical question, not a matter of
conscience, and it is measurable: the `imap-cleaner` evaluation harness crosses
every model against three redaction levels (raw, addresses pseudonymised, addresses
and names removed) precisely to find out what privacy costs in recall. An operator
choosing `redact.addresses` should be told the number, not left to guess.

## Consequences

Plugins stop being an operator-only extension mechanism and become the way herold
grows a *user-facing* feature without growing the binary. That is a real
architectural shift and it should be taken deliberately: the five plugin types
(REQ-PLUG-10) were scoped as server-side integrations, and this turns them into
something closer to apps.

The cost is a new compatibility surface. A view tree is a wire format; the
component vocabulary is an ABI. Adding a node type is additive and safe; changing
one is a breaking change with the same forward-only discipline as a DB migration.
Per STANDARDS section 8 the view-tree decoder gets a fuzz target, like every other
wire parser.

The benefit that pays for it, beyond ADR-0002: `options_schema` (REQ-PLUG-21) is
today a four-field stub that no UI reads and that the server does not even
validate -- the supervisor forwards operator options to the child and lets the
plugin check them (`internal/plugin/supervisor.go:522`). An `admin.panel` surface
makes plugin configuration a real, visible, validated operation instead of a TOML
block nobody can discover.

## What has to change

- **REQ-PLUG-10** -- plugin enablement and options move from system config to the
  DB. `system.toml` keeps only what is genuinely infra-owned.
- **REQ-PLUG-31** -- add `state.get` / `state.put` / `state.list` to the
  plugin-to-server method set.
- **REQ-PLUG-44** -- unchanged in intent, but the "except via server-mediated
  requests" clause now names the mechanism that satisfies it.
- **REQ-PLUG-60..62** -- add lifecycle states, removal-with-purge, and state
  schema versioning.
- **New: REQ-PLUG-80..** -- UI contributions, surfaces, the component vocabulary,
  the `ui.render` / `ui.action` methods, the manifest's `ui.kind` and permission
  list, and the reservation of `/plugin-assets/` and of `ui.kind: "component"` for
  tier 2.
- **New: a `PluginUI` JMAP datatype** on the capability registry
  (`internal/protojmap/registry.go`). The registry panics on duplicate method
  registration and is populated at construction; plugin-backed registration needs a
  late-binding, non-panicking variant.
- **REQ-FILT-30..32** -- recast from a global law into the default data grant of the
  LLM classifier plugin. The payload of `spam.classify` (and of every other
  mail-touching plugin method) becomes grant-shaped rather than fixed. This is an
  ABI change (REQ-PLUG-70..72).
- **New: REQ-PLUG-90..** -- data grants: the manifest's `[[data]]` block, the
  element vocabulary, the transforms, install-time consent, server-side projection,
  and re-consent on widened requests.
- **New store table** for plugin state, with an untyped JSON value column.
- **Suite** -- `SettingsView.svelte` `SECTIONS` becomes a runtime registry; one new
  `Capability` constant; a renderer for the component vocabulary.
- **Admin SPA** -- gains its first server-persisted settings surface.

## Open questions

- Does `message.detail` belong in v1, or does ADR-0002's rule trace live in the
  settings panel until the surface has a second consumer? Building an extension
  point for one caller is how extension points go wrong.
- Quota policy for plugin state: per-plugin, per-principal, or both, and what
  happens on exhaustion -- reject the write, or disable the plugin?
- Where does a principal's "what can read my mail?" view live -- the suite's
  settings, or a privacy surface of its own? It is the one part of the grant story
  with no natural home yet.
- Attachment *content* has no element. Some plugin will eventually want it (a virus
  scanner, most obviously), and it deserves its own decision rather than a quiet
  addition to the vocabulary.
- Does an `admin.panel` plugin get to see other principals' data? The spam filter
  does not need it; a directory plugin might. Leaving it out of v1 is the safe
  default.
- The suite is "content-blind on the wire" (`web/CLAUDE.md`). A view tree carrying
  a user's own policy text does not break that, but a `message.detail` panel
  quoting message content might. Worth checking against that invariant before
  building it.
- Tier 2: does a sandboxed frame with an opaque origin get a *theme*? Inheriting
  the suite's CSS variables across an opaque origin means the parent must push them
  over the bridge, and a plugin that ignores them looks like a foreign object
  embedded in the app. This is the question that decides whether tier 2 is pleasant
  or merely possible, and it should be answered before tier 2 is scheduled -- not
  during.
- Tier 2: does the postMessage bridge expose anything beyond "invoke an action on
  my own plugin"? Every addition to it is a hole in the opaque origin, and the
  pressure to add will be constant.
