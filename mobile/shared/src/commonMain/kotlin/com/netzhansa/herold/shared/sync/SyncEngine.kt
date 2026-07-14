package com.netzhansa.herold.shared.sync

/**
 * Sole reconciler between the local store and the JMAP server, and sole
 * owner of the offline outbox (docs/design/android/architecture/
 * 03-sync-and-state.md). The Compose UI never calls JMAP directly -- it
 * reads the local store and expresses intent; this engine drains the
 * outbox and reconciles via Foo/changes. Implementation lands with Phase 1
 * (docs/design/android/implementation-plan.md).
 */
interface SyncEngine {
    suspend fun start()

    suspend fun stop()
}
