# Environment variables

Design rule for all environment variables that herold reads.

## Naming convention

All operator-facing env vars follow `HEROLD_<SUBSYSTEM>_<PURPOSE>` in
SCREAMING_SNAKE_CASE. The `HEROLD_` prefix is mandatory. Examples:

| Variable | Subsystem | Purpose |
|---|---|---|
| `HEROLD_UI_SESSION_KEY` | `ui` | HMAC-SHA256 session-cookie signing key |
| `HEROLD_SYSTEM_CONFIG` | (CLI root) | Path to `system.toml` |
| `HEROLD_API_KEY` | (CLI root) | Admin REST API key for CLI commands |
| `HEROLD_PID_FILE` | (CLI root) | PID file path |
| `HEROLD_LOG_FILE` | (CLI root) | Server log file for support-bundle collection |
| `HEROLD_LOG_VERBOSE` | (CLI root) | Enable verbose debug logging |
| `HEROLD_ADMIN_BIND` | `admin` | (reserved; document when implemented) |
| `HEROLD_DATA_DIR` | (reserved) | (reserved; document when implemented) |

Subsystem tokens are short lowercase identifiers that match the package or
logical group (`ui`, `admin`, `smtp`, `imap`, `queue`, `acme`, etc.).

`NOTIFY_SOCKET` is a standard systemd convention; herold reads it but does not
own the name.

## Mandatory vs optional

Every env var is classified as one of:

- **Required** — the process refuses to start without it and the startup error
  names the variable explicitly. Document: what the value must look like, the
  error message text.
- **Optional with a default** — herold applies a built-in default when the
  variable is absent or unsuitable. Document: what the default is, and whether
  startup emits WARN or INFO when defaulting (prefer WARN for security-relevant
  defaults such as ephemeral session keys; INFO for benign operational defaults).

A variable MUST NOT be silently ignored.

## Startup logging

When herold falls back to a default because an Optional variable is absent or
invalid:

- Security-relevant defaults (e.g. ephemeral session signing key): emit at
  `WARN` once per startup. The message must name the variable the operator
  should set — the operator must be able to diagnose the issue without reading
  docs.
- Benign defaults: emit at `INFO` or omit entirely if the default is
  unambiguous and non-surprising.

Never emit more than one log line per variable per startup for the "using
default" condition.

## TOML override knobs

Some env var names are configurable via a TOML knob (e.g.
`[server.ui].signing_key_env`). These exist for environments where a secrets
manager mandates its own variable naming. The design rule is:

1. There is always a predefined default env var name the operator can use
   without any TOML configuration.
2. The TOML knob is an override for non-default deployments only. Most
   operators leave it unset.
3. The WARN / INFO log message always names the predefined default env var, not
   the TOML-configured override, so the message is self-contained for an
   operator who never read the config reference.

## Where env vars are documented

The single canonical reference table is in `docs/manual/admin/operate.mdoc` under
"Environment variables reference". Every operator-facing env var has a row
there. Keep the table sorted by variable name within each subsystem group.

## Test and dev-only variables

Variables that exist solely for the test harness or developer convenience
(e.g. `HEROLD_E2E_*`, `HEROLD_SPIKE`, `HEROLD_PG_DSN`) are:

- Never read by production code paths (only by `_test.go` files or explicit
  opt-in test helpers).
- Not listed in the operator reference table.
- Named with the `HEROLD_` prefix to avoid collisions, but otherwise not
  required to follow the subsystem convention.

## Adding a new env var checklist

1. Choose a name: `HEROLD_<SUBSYSTEM>_<PURPOSE>`.
2. Classify: Required or Optional.
3. If Optional: decide the default, decide the log level for the "using
   default" WARN/INFO.
4. Add a row to the reference table in `docs/manual/admin/operate.mdoc`.
5. If a TOML override knob is needed, follow the pattern in
   `[server.ui].signing_key_env`: the predefined name is the default; the TOML
   knob is named `<purpose>_env`; the WARN always names the predefined default.
6. Add the variable to this doc's examples table if it is a new subsystem.
