<script lang="ts">
  // Dashboard placeholder. The real implementation will aggregate data from:
  //   GET /api/v1/queue/stats     -- message queue counters
  //   GET /api/v1/audit           -- recent audit events (limit=10)
  //   GET /api/v1/domains         -- domain list (count)
  // These endpoints are documented in the Phase 2 audit at:
  //   docs/design/server/notes/phase-2-protoui-protoadmin-coverage-audit-2026-04-27.md
  // The aggregated dashboard view lands in a follow-up commit.
</script>

<div class="dashboard">
  <h1 class="page-title">Dashboard</h1>
  <p class="placeholder">Dashboard coming soon.</p>
  <p class="note">
    This view will aggregate queue statistics, recent audit events, and
    domain count from the protoadmin REST API. Implementation lands in a
    follow-up commit once the login/session flow is verified end-to-end.
  </p>
</div>

<style>
  .dashboard {
    max-width: 960px;
  }
  .page-title {
    font-size: var(--type-heading-03-size);
    line-height: var(--type-heading-03-line);
    font-weight: var(--type-heading-03-weight);
    color: var(--text-primary);
    margin: 0 0 var(--spacing-06);
  }
  .placeholder {
    font-size: var(--type-body-02-size);
    line-height: var(--type-body-02-line);
    color: var(--text-secondary);
    margin: 0 0 var(--spacing-04);
  }
  .note {
    font-size: var(--type-body-01-size);
    line-height: var(--type-body-01-line);
    color: var(--text-helper);
    margin: 0;
    max-width: 60ch;
  }
</style>
