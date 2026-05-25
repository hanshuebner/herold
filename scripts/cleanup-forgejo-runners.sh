#!/usr/bin/env bash
# cleanup-forgejo-runners.sh
#
# Drop accumulated dead `herold-runner-*` registrations from the
# Forgejo action_runner table. Run on outpost as a user with read
# access to /var/lib/forgejo/data/forgejo.db (i.e. the `git` user
# or root via sudo). The script prints a summary first; pass --apply
# as the first argument to actually execute the DELETE.
#
# Why this exists: Forgejo 14.0.5 has no REST endpoint that lists or
# bulk-deletes runners (only /runners/jobs and /runners/registration-
# token are exposed; the web UI talks to its own DB). Before the
# orchestrator picked up VM name reuse, every spawn registered a new
# unique runner name with Forgejo, so a few hundred dead `offline`
# entries accumulated. Live runners (= rows whose `last_online`
# heartbeat is recent) are preserved by the SQL filter.
#
# Forgejo can stay running; sqlite WAL mode permits concurrent reads
# and the action_runner table has no foreign-key constraints that
# the runtime cares about for offline rows.

set -euo pipefail

DB=${FORGEJO_DB:-/var/lib/forgejo/data/forgejo.db}
APPLY=${1:-}
# Anything not seen in the last 30 minutes is treated as dead. Live
# runners heartbeat every few seconds so this is generous.
DEAD_AFTER_SEC=$((30 * 60))

if [ ! -r "$DB" ]; then
  echo "cannot read $DB (try via sudo as the git user)" >&2
  exit 2
fi

now=$(date -u +%s)
cutoff=$((now - DEAD_AFTER_SEC))

# Summary first
python3 <<EOF
import sqlite3
con = sqlite3.connect("file:${DB}?mode=ro", uri=True)
cur = con.cursor()
total = cur.execute(
    "SELECT COUNT(*) FROM action_runner WHERE name LIKE 'herold-runner-%'"
).fetchone()[0]
dead = cur.execute(
    "SELECT COUNT(*) FROM action_runner "
    "WHERE name LIKE 'herold-runner-%' AND last_online < ?",
    (${cutoff},),
).fetchone()[0]
live = total - dead
print(f"herold-runner-* in action_runner: total={total} live={live} dead={dead}")
print(f"dead = last_online older than {${DEAD_AFTER_SEC}}s (cutoff unix={${cutoff}})")
EOF

if [ "$APPLY" != "--apply" ]; then
  echo
  echo "DRY RUN. Re-run with --apply to delete the dead rows."
  exit 0
fi

# Delete. action_runner has no fk pointing to it for offline rows;
# the related action_runner_token rows are cleaned by Forgejo on its
# own schedule.
sqlite3 "$DB" <<EOF
DELETE FROM action_runner
 WHERE name LIKE 'herold-runner-%' AND last_online < ${cutoff};
SELECT changes() AS deleted;
EOF
echo "done"
