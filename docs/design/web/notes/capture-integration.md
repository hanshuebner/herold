# Capture integration

How gmail-logger output feeds into the requirement docs.

## Workflow

1. Run gmail-logger for 5–7 working days. (Install per `../../gmail-logger/README.md`.)
2. From the popup, click **⚡ Analyze** to download a `gmail-analysis-*.json` report.
3. Apply the mappings below.
4. Update the affected docs' status from "placeholder" to "capture-enriched".

## Mappings

| Capture field | Target doc(s) | What to extract |
|---------------|---------------|-----------------|
| `top_actions` | `requirements/02-mail-basics.md`, plus the feature-area docs | Every action with count ≥ 5 should have at least one REQ. Add as `REQ-MAIL-7n+` if it isn't already specified elsewhere. |
| `top_workflows` (bigrams) | `requirements/12-workflows.md` | Each bigram with count ≥ 5 becomes a `REQ-WF-nn` entry. Pick the dominant trigger (first action) and outcome (second action). |
| `keyboard_vs_click` ratio | `requirements/10-keyboard.md` | Calibrates priorities. If the user is > 80% keyboard, all P1 bindings should be promoted to P0; if < 30%, P0 demotes to P1 except for `c`, `/`, and Escape. |
| Per-action `method` ratio | `requirements/10-keyboard.md` | An action with count ≥ 10 and ≥ 50% keyboard usage gets P0 priority for its shortcut. |
| `views_visited` | `requirements/01-data-model.md`, `requirements/05-categorisation.md`, `requirements/08-chat.md` | Confirms which view types must be implemented. A view type with 0 visits in a 5-day window is cut from v1. |
| `summary.sessions`, `summary.total_events` | `requirements/13-nonfunctional.md` | Sessions × events / day informs realistic load profile (`REQ-PERF-*` budgets). |

## What capture does NOT decide

- Security, accessibility, reliability requirements. These are policy, not usage.
- The data model (`requirements/01-data-model.md`). The Gmail ↔ JMAP mapping is fixed by the protocol, not by what the user does.
- Anything in `00-scope.md` non-goals. Capture data showing the user uses a non-goal feature is noted in `notes/open-questions.md` for explicit re-litigation; it does not silently flip scope.

## Re-running after scope changes

If capture data reshapes scope (e.g. a feature was thought low-priority and turns out to be the user's most-used action), update `00-scope.md` first, then revise the affected requirements docs. Do not let requirement edits implicitly redefine scope.

## Seeding the shortcut coach from capture

The shortcut coach (`../requirements/23-shortcut-coach.md`) starts every new principal at zero — no per-action history, no bigram store. Day-one the suite treats every action as `learning` and would hint about every shortcut on first invocation. That's annoying for a user who already knows half the Gmail-compatible shortcuts from years of use.

The capture data answers exactly that question: what's the user's existing keyboard fluency? `keyboard_vs_click` per action says which shortcuts they've internalised on Gmail. Bringing that history forward avoids re-tutoring and lets the coach focus on shortcuts the user genuinely doesn't use.

### One-time seed at first login

Run once, before or shortly after the user first opens the suite. Mechanically:

1. **Read the analysis JSON.** Either upload it through a settings affordance ("Import shortcut history from Gmail") or run a small CLI utility (TBD; could live in `apps/mail/scripts/seed-coach.ts` once the codebase exists) that POSTs the relevant rows to herold.
2. **Map gmail-logger action names to the suite action names.** Most match verbatim (`archive`, `compose_new`, `reply`, `nav_inbox`, etc.) since the gmail-logger's `ACTION_MAP` and `KEY_MAP` were sized to mirror what the suite would call the same actions. Document any divergences inline as the implementation lands; for v1 the mapping is direct.
3. **Translate per-action stats into `ShortcutCoachStat` rows.** For each action present in the capture analysis:
   - `keyboardCount90d` = `keyboard_vs_click[action].keyboard` (the absolute keyboard count over the capture window)
   - `mouseCount90d` = `keyboard_vs_click[action].click`
   - `keyboardCount14d` = `keyboardCount90d` if the capture window was ≤ 14 days, else `keyboardCount90d × (14 / capture-window-days)` as a linear approximation
   - `mouseCount14d` = `mouseCount90d` similarly
   - `lastKeyboardAt` = capture window's end (latest event with that action via keyboard, if recorded; else end-of-window)
   - `lastMouseAt` = same logic for mouse
   - `dismissCount` = 0
   - `dismissUntil` = null
4. **Issue one batched `ShortcutCoachStat/set { update: { ... } }` call** to herold via JMAP.

### Result

Day-one the suite sees the user as already-`internalised` for shortcuts they've used heavily on Gmail (e.g. `e` archive, `c` compose, `j`/`k` navigation if they use them) — no hints. Shortcuts the user used rarely or never start as `learning` and get hinted normally. Over the first week, the 14-day window rolls forward as the suite accumulates its own data and the seeded numbers age out — the user's actual the suite behaviour gradually replaces the Gmail-derived seed.

### Bigram store

The session-scoped bigram store (REQ-COACH-40..43) is in-memory and cannot be seeded directly. Optionally: at first launch, populate it with `top_workflows` from the capture analysis so the predictor has historical bigrams from the user's Gmail behaviour. The bigrams expire as the in-memory window fills with real the suite activity — typical session reaches 200 actions within a few hours of normal use. This optional bonus seeding makes the prediction smarter on day one but is not required.

### When to skip the seed

- The user explicitly opts out of the coach (REQ-SET-12 disabled before first login).
- The user resets coach data (REQ-COACH-72) — that wipes server-side state; re-seeding from old capture would defeat the purpose.
- Heavy mismatch between Gmail and the suite action sets (rare; the action vocabularies were aligned by design).
