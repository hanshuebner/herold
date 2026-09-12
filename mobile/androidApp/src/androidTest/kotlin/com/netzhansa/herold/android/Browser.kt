package com.netzhansa.herold.android

import androidx.test.uiautomator.By
import androidx.test.uiautomator.StaleObjectException
import androidx.test.uiautomator.UiDevice
import androidx.test.uiautomator.UiObject2
import androidx.test.uiautomator.Until
import java.util.regex.Pattern

/**
 * Drives herold's login page inside the browser the Custom Tab opened.
 * Chrome puts the page's form controls in the accessibility tree, which
 * is what UiAutomator reads: the inputs arrive as EditText and the
 * submit control as Button, in page order.
 *
 * The submit control is matched on its class as well as its label,
 * because the Custom Tab's own toolbar shows the page title - "Sign
 * in" - as a TextView above the page and a label-only match hits that
 * instead.
 */
object Browser {

    /**
     * Chrome's first-run screens. The harness completes them once
     * before the run (see mobile/README.md); this is the safety net
     * for a freshly wiped emulator.
     */
    fun dismissFirstRun(device: UiDevice) {
        val labels = listOf("Use without an account", "No thanks", "Accept & continue", "Got it", "Next")
        repeat(4) {
            val hit = device.wait(
                Until.findObject(By.clazz("android.widget.Button").text(Pattern.compile(labels.joinToString("|")))),
                FIRST_RUN_TIMEOUT_MS,
            ) ?: return
            runCatching { hit.click() }
            device.waitForIdle()
        }
    }

    /** Waits for the page's inputs and returns them in page order. */
    fun awaitFields(device: UiDevice, count: Int): List<UiObject2> {
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            val fields = device.findObjects(By.clazz("android.widget.EditText")).orEmpty()
            if (fields.size >= count) return fields
            device.waitForIdle()
            Thread.sleep(POLL_MS)
        }
        error("the page showed fewer than $count inputs")
    }

    /**
     * Fills the page's inputs in order and submits. Each field is
     * looked up again before it is written: typing into a web input
     * re-lays out the page, which stales a node handle taken earlier.
     */
    fun signIn(device: UiDevice, vararg values: String) {
        values.indices.forEach { index ->
            retrying { awaitFields(device, values.size)[index].text = values[index] }
            device.waitForIdle()
        }
        submit(device)
    }

    /** Runs [action], once more if the node it addressed went stale. */
    private fun retrying(action: () -> Unit) {
        try {
            action()
        } catch (stale: StaleObjectException) {
            Thread.sleep(POLL_MS)
            action()
        }
    }

    fun submit(device: UiDevice) {
        retrying {
            val button = device.wait(
                Until.findObject(By.clazz("android.widget.Button").text("Sign in")),
                TIMEOUT_MS,
            ) ?: error("the login page showed no Sign in button")
            button.click()
        }
        device.waitForIdle()
    }

    /** True once the page carries the six-digit code field as well. */
    fun awaitTotpField(device: UiDevice): Boolean {
        val deadline = System.currentTimeMillis() + TIMEOUT_MS
        while (System.currentTimeMillis() < deadline) {
            if (device.findObjects(By.clazz("android.widget.EditText")).orEmpty().size >= 3) return true
            Thread.sleep(POLL_MS)
        }
        return false
    }

    private const val TIMEOUT_MS = 30_000L

    /**
     * Short: on a browser whose first run is already behind it the
     * labels never appear and every launch would otherwise pay this.
     */
    private const val FIRST_RUN_TIMEOUT_MS = 4_000L
    private const val POLL_MS = 500L
}
