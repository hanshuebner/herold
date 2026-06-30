---
description: File a new Forgejo ticket in herold/herold (delegates to the ticket-clerk agent)
argument-hint: <bug report or feature request>
---

File a new ticket in the herold Forgejo repo (`herold/herold` at
code.netzhansa.com) for the following request:

$ARGUMENTS

Delegate this to the **ticket-clerk** agent via the Agent tool
(`subagent_type: "ticket-clerk"`). The clerk analyzes the request against the
codebase, checks for duplicates, writes a lean-house-style ticket, creates it
with `mcp__forgejo__issue_create`, and applies exactly one TYPE
(`bug`/`enhancement`) and one AREA (`webmail`/`server`) label. It does NOT fix
anything.

If $ARGUMENTS is empty, ask what the ticket should be about before dispatching.

When the agent returns, relay the new issue number, URL, the labels applied, and
the one-line classification rationale.
