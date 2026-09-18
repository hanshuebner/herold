package com.netzhansa.herold.shared.sync

import kotlin.test.Test
import kotlin.test.assertEquals

/** What the one dot says (REQ-AND-SYNC-30). */
class AppStatusTest {

    @Test
    fun connectedAndQuietIsIdle() {
        assertEquals(
            AppStatus.IDLE,
            appStatus(offline = false, sync = SyncStatus.Idle, pendingOutbox = 0, failedOutbox = 0),
        )
    }

    @Test
    fun aRunningSyncIsBusy() {
        assertEquals(
            AppStatus.BUSY,
            appStatus(offline = false, sync = SyncStatus.Syncing, pendingOutbox = 0, failedOutbox = 0),
        )
    }

    @Test
    fun aQueuedEntryIsBusy() {
        assertEquals(
            AppStatus.BUSY,
            appStatus(offline = false, sync = SyncStatus.Idle, pendingOutbox = 2, failedOutbox = 0),
        )
    }

    @Test
    fun aFailedSyncIsFailed() {
        assertEquals(
            AppStatus.FAILED,
            appStatus(
                offline = false,
                sync = SyncStatus.Failed("Mailbox/get refused"),
                pendingOutbox = 0,
                failedOutbox = 0,
            ),
        )
    }

    @Test
    fun aRefusedEntryIsFailed() {
        assertEquals(
            AppStatus.FAILED,
            appStatus(offline = false, sync = SyncStatus.Idle, pendingOutbox = 1, failedOutbox = 1),
        )
    }

    @Test
    fun withoutAConnectionTheIndicatorSaysOffline() {
        assertEquals(
            AppStatus.OFFLINE,
            appStatus(
                offline = true,
                sync = SyncStatus.Failed("connection refused"),
                pendingOutbox = 3,
                failedOutbox = 1,
            ),
        )
    }
}
