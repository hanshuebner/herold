package com.netzhansa.herold.android

import android.content.Context
import android.graphics.Bitmap
import android.graphics.Canvas
import android.util.TypedValue
import android.view.View
import android.view.ViewGroup
import android.widget.FrameLayout
import android.widget.TextView
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextInput
import androidx.compose.ui.unit.DpSize
import androidx.compose.ui.unit.dp
import androidx.core.content.pm.ShortcutManagerCompat
import androidx.glance.ExperimentalGlanceApi
import androidx.glance.appwidget.compose
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.UiDevice
import com.netzhansa.herold.android.home.ConversationShortcuts
import com.netzhansa.herold.android.home.InboxWidget
import com.netzhansa.herold.android.push.NotificationMute
import com.netzhansa.herold.android.ui.settings.TileAction
import com.netzhansa.herold.android.ui.settings.TileActionPreference
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import com.netzhansa.herold.shared.sync.toStoreRow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters
import java.io.File
import java.io.FileOutputStream

/**
 * The milestone 3b platform-integration acceptance (issue #362): the share
 * target, `mailto:`, the internal deep link and the Suite's https thread
 * URL, the home-screen widget, the Quick Settings tile and the launcher's
 * shortcuts, driven against an ephemeral herold from
 * `scripts/dev-instance.sh`.
 *
 * Every entry point is driven the way the platform drives it - an intent
 * from the shell, a tile click through `cmd statusbar`, the widget through
 * its own RemoteViews composition - rather than by calling into the app,
 * so what is asserted is the registration as well as the behaviour.
 *
 * The https VIEW intent names the package: App Link verification needs the
 * deployment origin to serve `/.well-known/assetlinks.json`, which it does
 * not yet (`docs/design/android/notes/server-contract.md`), and a
 * debug-signed build carries a different certificate in any case.
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class SystemIntegrationAcceptanceTest {

    @get:Rule
    val compose = createEmptyComposeRule()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val context: Context get() = instrumentation.targetContext

    private val app get() = context.applicationContext as HeroldApplication

    private val device get() = UiDevice.getInstance(instrumentation)

    private val packageName get() = context.packageName

    @Before
    fun signedIn() {
        grantNotificationPermission()
        NotificationMute.clear(context)
        device.pressHome()
        runBlocking {
            if (app.container.session.value == null) {
                val result = app.container.signInWithPassword(
                    DevInstance.baseUrl,
                    DevInstance.email,
                    DevInstance.password,
                    null,
                )
                assertTrue("sign-in failed: $result", result is SignInResult.Success)
            }
            app.container.session.value!!.syncEngine.syncAll()
        }
    }

    @Test
    fun t30_aSharedTextAndImageOpenComposeAndGoOutTogether() {
        val photo = File(context.cacheDir, "shared-photo.png").apply {
            writeBytes(PNG_BYTES)
            setReadable(true, false)
        }
        val subject = "Holiday photo ${System.currentTimeMillis()}"
        shellOut(
            "am start -a android.intent.action.SEND -t image/png -p $packageName " +
                "--es android.intent.extra.SUBJECT '$subject' " +
                "--es android.intent.extra.TEXT 'Sent from another app' " +
                "--eu android.intent.extra.STREAM file://${photo.absolutePath}",
        )

        awaitTag("compose-screen")
        awaitTag("attachment-ready-shared-photo.png")
        compose.onNodeWithTag("compose-subject").assertIsDisplayed()
        compose.captureScreen("30-share-to-compose")

        compose.onNodeWithTag("compose-to").performTextInput(DevInstance.recipientEmail + ",")
        compose.onNodeWithTag("compose-send").performClick()
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("compose-screen").fetchSemanticsNodes().isEmpty()
        }

        val delivered = awaitDelivered(subject)
        assertTrue(
            "the shared text must be the body, saw ${delivered.bodyText}",
            delivered.bodyText.orEmpty().contains("Sent from another app"),
        )
        val attachment = delivered.attachments.firstOrNull { it.name == "shared-photo.png" }
        assertNotNull("the shared file must arrive as an attachment", attachment)
        assertEquals(PNG_BYTES.size.toLong(), attachment!!.size)
    }

    @Test
    fun t31_aMailtoLinkOpensComposePrefilled() {
        device.pressHome()
        shellOut(
            "am start -a android.intent.action.VIEW -p $packageName " +
                "-d 'mailto:${DevInstance.recipientEmail}?subject=Lunch&body=At%20noon%3F'",
        )
        awaitTag("compose-screen")
        compose.onNodeWithTag("compose-to-chip-${DevInstance.recipientEmail}").assertIsDisplayed()
        compose.captureScreen("31-mailto-compose")
    }

    @Test
    fun t32_theInternalLinkAndTheSuiteUrlBothLandOnTheThread() {
        val seeded = seedThread("Deep link ${System.currentTimeMillis()}")

        device.pressHome()
        shellOut(
            "am start -a android.intent.action.VIEW -p $packageName " +
                "-d herold://thread/${seeded.accountId}/${seeded.threadId}",
        )
        awaitTag("thread-messages")
        compose.onNodeWithTag("thread-title").assertIsDisplayed()

        device.pressHome()
        // The Suite's own URL for the conversation. It carries no account,
        // so the shell resolves it from the local store.
        shellOut(
            "am start -a android.intent.action.VIEW -p $packageName " +
                "-d 'https://mail.netzhansa.com/#/mail/thread/${seeded.threadId}'",
        )
        awaitTag("thread-messages")
        compose.onNodeWithTag("thread-title").assertIsDisplayed()
        compose.captureScreen("32-app-link-thread")
    }

    @OptIn(ExperimentalGlanceApi::class)
    @Test
    fun t33_theWidgetRendersTheStoreAndFollowsNewMail() {
        device.pressHome()
        val before = widgetText()
        assertTrue("the widget must head itself with the inbox, saw $before", before.contains("Inbox"))
        assertTrue("the widget must state the unread count, saw $before", before.contains("unread"))

        val subject = "Widget mail ${System.currentTimeMillis()}"
        seedThread(subject)

        val after = widgetText()
        assertTrue("a new message must reach the widget, saw $after", after.contains(subject))
        captureWidget("33-widget")
    }

    @Test
    fun t34_theTileAndTheShortcutsAreThereAndAct() {
        val manifest = ShortcutManagerCompat.getShortcuts(
            context,
            ShortcutManagerCompat.FLAG_MATCH_MANIFEST,
        )
        assertTrue(
            "the launcher must offer the static Compose shortcut, saw ${manifest.map { it.id }}",
            manifest.any { it.id == "compose" },
        )
        val seeded = seedThread("Shortcut ${System.currentTimeMillis()}")
        compose.waitUntil(TIMEOUT_MS) {
            ShortcutManagerCompat.getDynamicShortcuts(context)
                .any { it.id == ConversationShortcuts.idFor(seeded.accountId, seeded.threadId) }
        }

        val tile = "$packageName/com.netzhansa.herold.android.home.HeroldTileService"
        TileActionPreference.remember(context, TileAction.MUTE_NOTIFICATIONS)
        shellOut("cmd statusbar add-tile $tile")
        shellOut("cmd statusbar click-tile $tile")
        compose.waitUntil(TIMEOUT_MS) { NotificationMute.isMuted(context) }
        assertTrue("the tile's mute must quieten the shade", NotificationMute.isMuted(context))
        shellOut("cmd statusbar click-tile $tile")
        compose.waitUntil(TIMEOUT_MS) { !NotificationMute.isMuted(context) }
        TileActionPreference.remember(context, TileAction.COMPOSE)
        shellOut("cmd statusbar remove-tile $tile")
    }

    /** Delivers a message and waits for it to reach the local store. */
    private fun seedThread(subject: String): Email {
        DevInstance.deliverMail(subject, body = "Body of $subject.")
        repeat(DELIVERY_POLLS) {
            val found = runBlocking {
                app.container.session.value!!.syncEngine.syncAll()
                app.container.store.inboxEmails().first().firstOrNull { it.subject == subject }
            }
            if (found != null) return found
            Thread.sleep(DELIVERY_POLL_MS)
        }
        error("the seeded message \"$subject\" never reached the local store")
    }

    /** The message as the recipient's account received it. */
    private fun awaitDelivered(subject: String): Email = runBlocking {
        val recipient = DevInstance.recipientClient()
        val accountId = recipient.session().mailAccountId!!
        val inbox = recipient.mailboxGet(accountId).list.first { it.role == "inbox" }.id
        repeat(DELIVERY_POLLS) {
            val ids = recipient.emailQueryInbox(accountId, inbox, 20)
            val found = recipient.emailGet(accountId, ids, withBody = true).list
                .map { it.toStoreRow(accountId) }
                .firstOrNull { it.subject == subject }
            if (found != null) return@runBlocking found
            Thread.sleep(DELIVERY_POLL_MS)
        }
        error("no message with subject \"$subject\" reached ${DevInstance.recipientEmail}")
    }

    /**
     * The widget as the launcher inflates it: its own composition, applied
     * to a view, read back as text.
     */
    @OptIn(ExperimentalGlanceApi::class)
    private fun widgetText(): String = textOf(widgetView())

    @OptIn(ExperimentalGlanceApi::class)
    private fun widgetView(): View = runBlocking {
        val views = InboxWidget().compose(context, size = DpSize(240.dp, 240.dp))
        var view: View? = null
        instrumentation.runOnMainSync {
            val parent = FrameLayout(context)
            val inflated = views.apply(context, parent)
            val widthPx = TypedValue.applyDimension(
                TypedValue.COMPLEX_UNIT_DIP,
                WIDGET_DP,
                context.resources.displayMetrics,
            ).toInt()
            inflated.measure(
                View.MeasureSpec.makeMeasureSpec(widthPx, View.MeasureSpec.EXACTLY),
                View.MeasureSpec.makeMeasureSpec(widthPx, View.MeasureSpec.EXACTLY),
            )
            inflated.layout(0, 0, inflated.measuredWidth, inflated.measuredHeight)
            view = inflated
        }
        instrumentation.waitForIdleSync()
        view ?: error("the widget produced no view")
    }

    /** Writes a PNG of the inflated widget where the harness collects it. */
    @OptIn(ExperimentalGlanceApi::class)
    private fun captureWidget(name: String) {
        val view = widgetView()
        val bitmap = Bitmap.createBitmap(
            view.measuredWidth.coerceAtLeast(1),
            view.measuredHeight.coerceAtLeast(1),
            Bitmap.Config.ARGB_8888,
        )
        instrumentation.runOnMainSync { view.draw(Canvas(bitmap)) }
        val staged = File(context.getExternalFilesDir(null), "$name.png")
        FileOutputStream(staged).use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        shellOut("mkdir -p $SHOT_DIR")
        shellOut("cp ${staged.absolutePath} $SHOT_DIR/$name.png")
    }

    private fun textOf(view: View): String = buildString {
        fun walk(node: View) {
            if (node is TextView) append(node.text).append('\n')
            if (node is ViewGroup) {
                for (index in 0 until node.childCount) walk(node.getChildAt(index))
            }
        }
        walk(view)
    }

    private fun awaitTag(tag: String) {
        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag(tag).fetchSemanticsNodes().isNotEmpty()
        }
    }

    private companion object {
        const val TIMEOUT_MS = 30_000L
        const val DELIVERY_POLLS = 30
        const val DELIVERY_POLL_MS = 1_000L
        const val WIDGET_DP = 240f

        /** A 64x64 solid PNG, small enough to skip the image size choice. */
        val PNG_BYTES: ByteArray = android.util.Base64.decode(
            "iVBORw0KGgoAAAANSUhEUgAAAEAAAABACAIAAAAlC+aJAAAAeklEQVR4nO3PUQkAIBTAwBfHTCY2" +
                "liH8OITBAtxm7fN1wwUNaEEDWtCAFjSgBQ1oQQNa0IAWNKAFDWhBA1rQgBY0oAUNaEEDWtCAFjSg" +
                "BQ1oQQNa0IAWNKAFDWhBA1rQgBY0oAUNaEEDWtCAFjSgBQ1oQQNa0IAWPHYBWQmhLScUAZAAAAAA" +
                "SUVORK5CYII=",
            android.util.Base64.DEFAULT,
        )
    }
}
