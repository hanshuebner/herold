package com.netzhansa.herold.android.diag

import android.app.Activity
import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.content.ContextCompat
import com.netzhansa.herold.android.AppContainer
import com.netzhansa.herold.android.BuildConfig
import com.netzhansa.herold.android.SessionScope
import com.netzhansa.herold.shared.compose.ComposeResult
import com.netzhansa.herold.shared.diag.BugBundleWriter
import com.netzhansa.herold.shared.diag.BugCapture
import com.netzhansa.herold.shared.diag.BugSubmission
import com.netzhansa.herold.shared.diag.DeviceFacts
import com.netzhansa.herold.shared.diag.OutboxLine
import com.netzhansa.herold.shared.diag.OutboxSummary
import com.netzhansa.herold.shared.diag.PushFacts
import com.netzhansa.herold.shared.diag.SyncFacts
import com.netzhansa.herold.shared.outbox.OutboxState
import com.netzhansa.herold.shared.sync.SyncStatus

/**
 * Takes the snapshot a report is built from, at the moment of the
 * gesture and before the sheet covers the screen (REQ-AND-SYS-51), and
 * hands the finished bundle to the outbox.
 *
 * Everything it reads is state the app already holds: the route the
 * shell is on, what the build is, what the reconciler last did, what is
 * in the queue and how push is wired. No message body and no subject
 * goes in, and the bearer credential goes nowhere at all - not even
 * into the private part, which exists for the ids that make a bug
 * reproducible.
 */
class BugReportController(private val container: AppContainer) {

    /**
     * Captures the window and the app's state. [route] and [arguments]
     * come from the navigation host, which is what knows where the user
     * is.
     */
    suspend fun capture(
        activity: Activity,
        session: SessionScope?,
        route: String,
        arguments: Map<String, String>,
        withScreenshot: Boolean,
    ): BugCapture {
        val context = activity.applicationContext
        val shot = if (withScreenshot) WindowCapture.png(activity) else null
        val accounts = runCatching { container.store.accountList() }.getOrDefault(emptyList())
        val registration = runCatching { container.store.pushRegistration() }.getOrNull()
        val queue = runCatching { container.outbox.list() }.getOrDefault(emptyList())
        val principal = runCatching { container.tokenStore.principal() }.getOrNull()

        return BugCapture(
            route = route,
            routeArguments = arguments,
            accountScope = container.accountScope.value,
            threadId = arguments["threadId"],
            principal = principal,
            serverUrl = session?.baseUrl,
            accountIds = accounts.map { it.id },
            device = deviceFacts(),
            sync = syncFacts(session),
            outbox = outboxSummary(queue),
            push = pushFacts(context, registration?.transport),
            logs = DiagLog.ring.lines(),
            screenshots = listOfNotNull(shot),
            sessionDetails = sessionDetails(session, principal, registration?.deviceClientId, accounts.map { it.id }),
        )
    }

    /**
     * Builds the bundle and queues it for the server's bug-reports
     * endpoint. The account it goes out on is the one in scope, or the
     * primary.
     */
    suspend fun send(submission: BugSubmission, capture: BugCapture, holdMs: Long): ComposeResult {
        val accountId = container.accountScope.value
            ?: container.store.accountList().firstOrNull { it.isPrimary }?.id
            ?: container.store.accountList().firstOrNull()?.id
            ?: return ComposeResult.Failed("No account to send the report from")
        val bundle = BugBundleWriter.build(submission, capture, System.currentTimeMillis())
        val result = container.bugReports.queue(bundle, accountId, holdMs)
        if (result is ComposeResult.Queued) {
            DiagLog.i(TAG, "bug report queued as entry ${result.entryId}")
        }
        return result
    }

    private fun deviceFacts(): DeviceFacts = DeviceFacts(
        appVersion = BuildConfig.VERSION_NAME,
        appCommit = BuildConfig.GIT_COMMIT,
        androidVersion = Build.VERSION.RELEASE.orEmpty(),
        sdkInt = Build.VERSION.SDK_INT,
        manufacturer = Build.MANUFACTURER.orEmpty(),
        model = Build.MODEL.orEmpty(),
    )

    private fun syncFacts(session: SessionScope?): SyncFacts {
        val status = session?.syncEngine?.status?.value
        return SyncFacts(
            state = when (status) {
                null -> "no session"
                SyncStatus.Idle -> "idle"
                SyncStatus.Syncing -> "syncing"
                is SyncStatus.Failed -> "failed"
            },
            lastError = (status as? SyncStatus.Failed)?.message,
            offline = container.offline.value,
        )
    }

    private fun outboxSummary(entries: List<com.netzhansa.herold.shared.outbox.OutboxEntry>): OutboxSummary =
        OutboxSummary(
            queued = entries.count { it.state == OutboxState.QUEUED },
            sending = entries.count { it.state == OutboxState.SENDING },
            failed = entries.count { it.state == OutboxState.FAILED },
            entries = entries.map { entry ->
                OutboxLine(
                    kind = entry.kind.name,
                    state = entry.state.name,
                    attempts = entry.attempts,
                    label = entry.label,
                    lastError = entry.lastError,
                )
            },
        )

    private fun pushFacts(context: Context, registeredTransport: String?): PushFacts {
        val push = container.push
        return PushFacts(
            choice = push.choice.stored,
            transport = push.transport()?.wire,
            distributor = runCatching { push.distributor() }.getOrNull(),
            registered = registeredTransport != null,
            permissionGranted = notificationsAllowed(context),
        )
    }

    private fun notificationsAllowed(context: Context): Boolean =
        Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU ||
            ContextCompat.checkSelfPermission(
                context,
                android.Manifest.permission.POST_NOTIFICATIONS,
            ) == PackageManager.PERMISSION_GRANTED

    /**
     * What goes under `private/` when the maintainer asks for it: the
     * ids that let a bug be reproduced against the same account and the
     * same subscription. The bearer token is not among them; it stays
     * in Keystore-backed storage and is never written anywhere else.
     */
    private suspend fun sessionDetails(
        session: SessionScope?,
        principal: String?,
        deviceClientId: String?,
        accountIds: List<String>,
    ): Map<String, String> = buildMap {
        principal?.let { put("principal", it) }
        session?.baseUrl?.let { put("serverUrl", it) }
        runCatching { container.tokenStore.grantId() }.getOrNull()?.let { put("grantId", it) }
        deviceClientId?.let { put("pushDeviceClientId", it) }
        if (accountIds.isNotEmpty()) put("accountIds", accountIds.joinToString(","))
        put("note", "no bearer token is included; it does not leave Keystore-backed storage")
    }

    private companion object {
        const val TAG = "herold.bugreport"
    }
}
