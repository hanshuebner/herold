# 06 — Topology

*(Revised 2026-04-24: multi-node is a non-goal. Single node only.)*

Single-node. That's the whole topology.

## v1 shape — the only shape

```
          ┌───────────────────────────────┐
          │                               │
          │         herold              │
          │         + plugins             │
          │                               │
          │   ┌───────────────────────┐   │
          │   │ store (meta+blob+FTS) │   │
          │   └───────────────────────┘   │
          │                               │
          └───────────────────────────────┘
                       ▲
                       │ local filesystem
                       │
                 /var/lib/herold
                  /etc/herold/system.toml
```

One process (plus plugin children). One host. One data directory. No external dependencies beyond DNS and ACME.

At the scale target (1k mailboxes, 10k msg/day each way, 2 TB, 1k concurrent sessions), one modern node has comfortable headroom. No scenario in v1's scope justifies the complexity of multi-node.

## What "no multi-node" costs

- Single point of failure at the host level. Operators mitigate with:
  - Hypervisor HA (VMware, Proxmox HA, live migration).
  - Cold standby with backup restore (minutes of RTO).
  - DNS failover (MX weighted, secondary MX at a different provider).
- No read-scaling. Not a concern at target scale.
- No geographic distribution. Not a concern for small-org self-host.

What it buys:
- Trivial backup: `herold diag backup`.
- Trivial restore.
- Capacity planning = one machine.
- No consensus bugs possible.
- No split-brain scenarios to defend against.
- No `kill -9` races across replicas.
- Reading the code: no partial-network-partition reasoning.

## Deployment patterns

### Bare VM / baremetal

```
  systemd: herold.service
    → /etc/herold/system.toml
    → /var/lib/herold/
      ├─ store/
      ├─ blobs/
      ├─ fts/
      ├─ queue/
      ├─ certs/
      └─ plugins/          # installed plugin executables
```

- Packaging: `.deb` / `.rpm`.
- Unit file with hardening (REQ-NFR-83).
- Logs to journald (stdout) by default.

### Container (Docker)

- Image: single binary in a minimal base (debian-slim).
- First-party plugins bundled in the image.
- Non-root user.
- Volumes: `/etc/herold/` (system config), `/var/lib/herold/` (data, app state, queue, certs).
- Ports: 25, 465, 587, 143, 993, 4190, 443, 80 (ACME HTTP-01 if used), 8080 (admin).
- Healthchecks: `/healthz/live`, `/healthz/ready`.

### Kubernetes

- **StatefulSet** with `replicas: 1`. Not Deployment — we need stable storage.
- `PersistentVolumeClaim` for `/var/lib/herold` (ReadWriteOnce).
- `ConfigMap` for system config. Application config is inside the PVC (DB); it's not a ConfigMap concern.
- `Service` type `LoadBalancer` or `NodePort` for external SMTP/IMAP/JMAP. TLS terminated in the pod (not offloaded — mail protocols don't cleanly support that).
- `HorizontalPodAutoscaler` not applicable (single replica).
- Rolling updates = brief unavailability (minutes).

### DR via cold standby

Common pattern and sufficient for v1's target audience:

1. Primary runs.
2. Backup every N minutes (`herold diag backup` → S3 / remote).
3. Secondary VM / Pod stays cold.
4. On primary failure: start secondary, restore latest backup, update DNS.
5. RTO: minutes. RPO: minutes.

Not five-nines; not meant to be.

## Third-party HA with v1 (operator's problem, not ours)

Some operators need better than cold-standby. Viable options without server-side changes:

- **Shared block storage + Pacemaker/Corosync.** Mount data dir on shared storage; one node active. On failover, mount on other node, start service. Seconds-to-minutes RTO.
- **DRBD + failover.** Block-level replication.
- **ZFS send/recv to warm standby.** Incremental snapshots.

These are ops patterns, not server features. We don't automate them and we don't break them.

## What we will NOT build

- Replication (anything).
- Leader election.
- Shared metadata backend accessed from multiple server instances.
- Shared blob backend (S3). Even as "phase 3", no.
- Cross-node state-change feed.
- Anycast DNS integration.
- Read-only replicas.
- Multi-region HA.

If a future project wants these, it's a different project. Herold's scope is explicitly the 1k-mailbox small-org single-node mail server. Anything else defeats the design.

## Scale-wall honesty

The explicit scale ceiling is:

- ~1k active mailboxes (more in practice — Sieve is per-user, IMAP IDLE is the concurrency constraint, quotas are bounded).
- ~10k msg/day each way (LLM classification at this rate is ~7/min — trivially handled by any small model).
- 2 TB of data (SQLite + filesystem — well within operational limits).
- 1k concurrent IMAP/JMAP sessions (goroutines — bounded by memory, 8–16 KiB stack per idle session).

At 10× these numbers, the design still works but specific tuning is needed (LLM concurrency, SQLite busy timeouts, indexing worker count). At 100×, it breaks — and that's OK. That's not our scope.
