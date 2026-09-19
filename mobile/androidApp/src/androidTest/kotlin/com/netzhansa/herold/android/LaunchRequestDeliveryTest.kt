package com.netzhansa.herold.android

import android.os.Looper
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.navigation.NavHostController
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.android.links.LaunchRequest
import com.netzhansa.herold.shared.links.AppDestination
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotSame
import org.junit.Assert.assertNull
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * How a launch request reaches the navigation graph (issue #434).
 *
 * A share, a `mailto:`, a deep link or an App Link is delivered by a
 * coroutine that has been through a store read, so it resumes on
 * whatever dispatcher answered it, while the NavController belongs to
 * the main thread. The delivery is driven from a background thread here
 * for exactly that reason: it must navigate rather than raise
 * "addObserver must be called on the main thread".
 *
 * The class stands its own navigation graph up and writes no account
 * state, so it runs in any position of the suite and twice over
 * (issue #414).
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class LaunchRequestDeliveryTest {

    @get:Rule
    val compose = createComposeRule()

    private val app
        get() = InstrumentationRegistry.getInstrumentation()
            .targetContext.applicationContext as HeroldApplication

    private lateinit var navController: NavHostController

    private fun standUpGraph() {
        compose.setContent {
            navController = rememberNavController()
            NavHost(navController = navController, startDestination = "inbox") {
                composable("inbox") { Box(Modifier.fillMaxSize().testTag("graph-inbox")) }
                composable("thread/{accountId}/{threadId}") {
                    Box(Modifier.fillMaxSize().testTag("graph-thread"))
                }
                composable("compose-handoff") { Box(Modifier.fillMaxSize().testTag("graph-compose")) }
            }
        }
        compose.waitForIdle()
    }

    private fun deliverOffTheMainThread(pending: LaunchRequest, queue: MutableStateFlow<LaunchRequest?>) =
        runBlocking(Dispatchers.IO) {
            assertNotSame(
                "the delivery has to be driven off the main thread to mean anything",
                Looper.getMainLooper().thread,
                Thread.currentThread(),
            )
            deliverLaunchRequest(pending, app.container, navController, queue)
        }

    @Test
    fun t10_aRequestDeliveredOffTheMainThreadOpensItsDestination() {
        standUpGraph()
        val queue = MutableStateFlow<LaunchRequest?>(null)
        val pending = LaunchRequest(AppDestination.Thread("acct-a", "thread-1"))
        queue.value = pending

        deliverOffTheMainThread(pending, queue)

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("graph-thread").fetchSemanticsNodes().isNotEmpty()
        }
        val entry = navController.currentBackStackEntry
        assertEquals("thread/{accountId}/{threadId}", entry?.destination?.route)
        assertEquals("acct-a", entry?.arguments?.getString("accountId"))
        assertEquals("thread-1", entry?.arguments?.getString("threadId"))
        assertNull("the request is taken off the queue once it is delivered", queue.value)
    }

    @Test
    fun t20_aRequestThatArrivedBehindTheOneBeingDeliveredStays() {
        standUpGraph()
        val queue = MutableStateFlow<LaunchRequest?>(null)
        val delivered = LaunchRequest(AppDestination.Thread("acct-a", "thread-1"))
        val behind = LaunchRequest(AppDestination.Compose())
        // What the shell sees when a second intent arrives while the
        // first is still being resolved: the queue already holds the
        // newer request when the older one lands.
        queue.value = behind

        deliverOffTheMainThread(delivered, queue)

        compose.waitUntil(TIMEOUT_MS) {
            compose.onAllNodesWithTag("graph-thread").fetchSemanticsNodes().isNotEmpty()
        }
        assertEquals("the newer request is still there to be delivered", behind, queue.value)
    }

    private companion object {
        const val TIMEOUT_MS = 10_000L
    }
}
