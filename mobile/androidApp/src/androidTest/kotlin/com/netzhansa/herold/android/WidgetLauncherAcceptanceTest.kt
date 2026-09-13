package com.netzhansa.herold.android

import android.appwidget.AppWidgetManager
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.By
import androidx.test.uiautomator.UiDevice
import androidx.test.uiautomator.Until
import com.netzhansa.herold.android.home.InboxWidget
import com.netzhansa.herold.android.home.InboxWidgetReceiver
import com.netzhansa.herold.shared.auth.SignInResult
import com.netzhansa.herold.shared.domain.Email
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith

/**
 * The home-screen widget as a launcher shows it (issue #375): pinned onto
 * the launcher's home screen and read back out of the launcher's own view
 * hierarchy, so what is asserted is what the user sees rather than a
 * RemoteViews composition inflated with an open height.
 */
@RunWith(AndroidJUnit4::class)
class WidgetLauncherAcceptanceTest {

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    private val context: Context get() = instrumentation.targetContext

    private val app get() = context.applicationContext as HeroldApplication

    private val device get() = UiDevice.getInstance(instrumentation)

    @Before
    fun signedIn() {
        grantNotificationPermission()
        device.pressHome()
        runBlocking {
            app.container.signOut()
            val result = app.container.signInWithPassword(
                DevInstance.baseUrl,
                DevInstance.email,
                DevInstance.password,
                null,
            )
            assertTrue("sign-in failed: $result", result is SignInResult.Success)
            app.container.session.value!!.syncEngine.syncAll()
        }
    }

    @Test
    fun t36_theWidgetOnTheLauncherShowsTheUnreadCountAndItsThreadRows() {
        val subject = "Widget row ${System.currentTimeMillis()}"
        seedThread(subject)
        runBlocking { InboxWidget.refresh(context) }

        placeWidget()

        showWidgetPage()
        val painted = awaitWidget { device.hasObject(By.textStartsWith("Widget row")) }
        captureDeviceScreen("36-widget-on-launcher")
        assertTrue(
            "the widget must state the unread count on the launcher",
            device.hasObject(By.textContains("unread")),
        )
        assertTrue(
            "the widget must show its thread rows on the launcher, expected \"$subject\"",
            painted,
        )
    }

    /**
     * Waits for the launcher to show what the store holds: the widget's
     * own repaint runs in a Glance session the host schedules, so the
     * first frame after a placement can still carry the older snapshot.
     */
    private fun awaitWidget(condition: () -> Boolean): Boolean {
        val deadline = System.currentTimeMillis() + PLACE_TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            if (condition()) return true
            runBlocking { InboxWidget.refresh(context) }
            device.waitForIdle()
            Thread.sleep(POLL_MS)
        }
        return condition()
    }

    /**
     * Brings the launcher page the widget was placed on into view: a
     * pinned widget lands on the first page with room, which is not
     * necessarily the one Home returns to.
     */
    private fun showWidgetPage() {
        device.pressHome()
        repeat(HOME_PAGES) {
            device.wait(Until.hasObject(By.pkg(context.packageName)), PAGE_TIMEOUT_MS)
            if (device.hasObject(By.pkg(context.packageName))) return
            device.swipe(
                device.displayWidth * 4 / 5,
                device.displayHeight / 2,
                device.displayWidth / 5,
                device.displayHeight / 2,
                10,
            )
            device.waitForIdle()
        }
        error("the widget is on no launcher page")
    }

    /**
     * Pins the widget through the launcher's own flow: the request raises
     * the launcher's confirmation, which is accepted here the way the user
     * accepts it.
     */
    private fun placeWidget() {
        val manager = AppWidgetManager.getInstance(context)
        val provider = ComponentName(context, InboxWidgetReceiver::class.java)
        if (manager.getAppWidgetIds(provider).isNotEmpty()) return
        assertTrue("the launcher must support pinning", manager.isRequestPinAppWidgetSupported)

        // The request is only granted to a foregrounded app.
        context.startActivity(
            Intent(context, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
        )
        device.wait(Until.hasObject(By.pkg(context.packageName).depth(0)), PLACE_TIMEOUT_MS)
        assertTrue("the pin request was refused", manager.requestPinAppWidget(provider, null, null))

        val confirm = CONFIRM_LABELS.firstNotNullOfOrNull { label ->
            device.wait(Until.findObject(By.text(java.util.regex.Pattern.compile(label, CASE_INSENSITIVE))), CONFIRM_TIMEOUT_MS)
        }
        confirm?.click()
        device.waitForIdle()
        val deadline = System.currentTimeMillis() + PLACE_TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            if (manager.getAppWidgetIds(provider).isNotEmpty()) return
            Thread.sleep(POLL_MS)
        }
        error("the widget never reached the launcher")
    }

    /** Delivers one message and waits for it to reach the local store. */
    private fun seedThread(subject: String): Email {
        DevInstance.deliverMail(subject = subject, body = "<p>Widget row body.</p>")
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

    private companion object {
        const val PLACE_TIMEOUT_MS = 20_000L
        const val CONFIRM_TIMEOUT_MS = 5_000L
        const val POLL_MS = 500L
        const val PAGE_TIMEOUT_MS = 3_000L
        const val HOME_PAGES = 5
        const val DELIVERY_POLLS = 20
        const val DELIVERY_POLL_MS = 1_000L
        const val CASE_INSENSITIVE = java.util.regex.Pattern.CASE_INSENSITIVE

        /** What a launcher labels the button that accepts a pin request. */
        val CONFIRM_LABELS = listOf("Add automatically", "Add to home screen", "Add")
    }
}
