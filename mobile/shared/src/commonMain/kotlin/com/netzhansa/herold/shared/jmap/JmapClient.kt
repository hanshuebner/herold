package com.netzhansa.herold.shared.jmap

import com.netzhansa.herold.shared.domain.Mailbox
import io.ktor.client.HttpClient

/**
 * Typed JMAP-over-bearer-token client
 * (docs/design/android/architecture/02-jmap-client.md). This is the Phase 0
 * seam: the session descriptor fetch, capability pinning, and the
 * Mailbox/get call that backs the mailbox list land in the same phase as the
 * auth bootstrap (docs/design/android/architecture/01-system-overview.md,
 * "Bootstrap"). The UI never talks to this client directly -- only the sync
 * engine does (docs/design/android/architecture/03-sync-and-state.md).
 */
class JmapClient(
    private val httpClient: HttpClient,
    private val sessionUrl: String,
) {
    suspend fun fetchMailboxes(): List<Mailbox> {
        throw NotImplementedError(
            "JmapClient.fetchMailboxes: Mailbox/get over bearer auth lands with the " +
                "Phase 0 authenticate-and-render increment",
        )
    }
}
