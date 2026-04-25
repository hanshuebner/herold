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
