package com.netzhansa.herold.android

import android.app.Activity
import android.view.View
import android.view.ViewGroup
import android.webkit.WebView
import androidx.test.platform.app.InstrumentationRegistry
import org.json.JSONObject
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * Reading where the composer's caret actually sits (issue #453).
 *
 * The body is a `contenteditable` in a WebView, so the caret is invisible
 * to the Compose tree. A check that wants to know whether the line being
 * typed is on screen asks the page for the caret's rectangle and maps it
 * into window pixels with the view's own position, which is the same
 * coordinate space the composer's viewport is measured in.
 */
data class CaretProbe(
    /** The caret's line, in window pixels. */
    val top: Float,
    val bottom: Float,
    /** What the page's own viewport is, for checking the pixel mapping. */
    val pageViewportHeightPx: Float,
    val documentHeightPx: Float,
    /** How far the page has scrolled itself, in device pixels. */
    val pageScrollPx: Float,
    /** The view's height and position in the window, in pixels. */
    val viewTop: Float,
    val viewHeight: Float,
    /** What the page's visual viewport does, which the renderer moves on its own. */
    val visualOffsetTopPx: Float,
    val visualHeightPx: Float,
    val visualScale: Float,
    /** How far the view itself has been scrolled by the renderer. */
    val viewScrollY: Float,
) {
    override fun toString(): String =
        "caret top=$top bottom=$bottom viewTop=$viewTop viewHeight=$viewHeight " +
            "pageViewport=$pageViewportHeightPx document=$documentHeightPx pageScroll=$pageScrollPx " +
            "visualOffsetTop=$visualOffsetTopPx visualHeight=$visualHeightPx scale=$visualScale " +
            "viewScrollY=$viewScrollY"
}

/** The composer's body editor, found in the activity's view tree. */
fun Activity.bodyWebView(): WebView =
    findWebView(window.decorView) ?: error("the composer's editor WebView is not in the view tree")

private fun findWebView(view: View): WebView? = when {
    view is WebView -> view
    view is ViewGroup -> (0 until view.childCount).firstNotNullOfOrNull { findWebView(view.getChildAt(it)) }
    else -> null
}

/** Where the caret is right now, in the window's coordinate space. */
fun WebView.caretProbe(): CaretProbe {
    val answer = JSONObject(evaluate(CARET_SCRIPT))
    check(answer.getBoolean("ok")) { "the editor holds no selection" }
    val ratio = answer.getDouble("ratio").toFloat()
    val location = IntArray(2)
    var height = 0
    var scrolled = 0
    InstrumentationRegistry.getInstrumentation().runOnMainSync {
        getLocationInWindow(location)
        height = this.height
        scrolled = this.scrollY
    }
    val viewTop = location[1].toFloat()
    // The renderer moves the visual viewport on its own to chase the
    // caret, which leaves the layout-viewport rectangle the page reports
    // offset from what is actually drawn; taking that offset out is what
    // puts the caret where the pixels are.
    val visualOffset = answer.getDouble("visualOffsetTop").toFloat() * ratio
    return CaretProbe(
        top = viewTop - visualOffset + answer.getDouble("top").toFloat() * ratio,
        bottom = viewTop - visualOffset + answer.getDouble("bottom").toFloat() * ratio,
        pageViewportHeightPx = answer.getDouble("clientHeight").toFloat() * ratio,
        documentHeightPx = answer.getDouble("scrollHeight").toFloat() * ratio,
        pageScrollPx = answer.getDouble("pageY").toFloat() * ratio,
        viewTop = viewTop,
        viewHeight = height.toFloat(),
        visualOffsetTopPx = visualOffset,
        visualHeightPx = answer.getDouble("visualHeight").toFloat() * ratio,
        visualScale = answer.getDouble("visualScale").toFloat(),
        viewScrollY = scrolled.toFloat(),
    )
}

/** Runs a script in the page and hands back what it evaluated to. */
fun WebView.evaluate(script: String, timeoutMs: Long = 10_000L): String {
    val latch = CountDownLatch(1)
    var result = ""
    InstrumentationRegistry.getInstrumentation().runOnMainSync {
        evaluateJavascript(script) { value ->
            result = value.orEmpty()
            latch.countDown()
        }
    }
    check(latch.await(timeoutMs, TimeUnit.MILLISECONDS)) { "the editor did not answer: $script" }
    return result
}

/**
 * The caret's line box in page coordinates. A collapsed range answers
 * with a zero-width rectangle where the caret blinks; where it answers
 * with nothing, the block the caret sits in is the line.
 */
private val CARET_SCRIPT = """
    (function () {
      var selection = window.getSelection();
      if (!selection || selection.rangeCount === 0) { return {ok: false}; }
      var range = selection.getRangeAt(0);
      var rects = range.getClientRects();
      var rect = rects.length > 0 ? rects[0] : null;
      if (!rect || (rect.top === 0 && rect.bottom === 0)) {
        var node = range.startContainer;
        var element = node.nodeType === 1 ? node : node.parentElement;
        rect = element ? element.getBoundingClientRect() : null;
      }
      if (!rect) { return {ok: false}; }
      var doc = document.scrollingElement || document.documentElement;
      var visual = window.visualViewport || {offsetTop: 0, height: doc.clientHeight, pageTop: 0, scale: 1};
      return {
        ok: true,
        top: rect.top,
        bottom: rect.bottom,
        ratio: window.devicePixelRatio,
        pageY: window.scrollY,
        clientHeight: doc.clientHeight,
        scrollHeight: doc.scrollHeight,
        visualOffsetTop: visual.offsetTop,
        visualHeight: visual.height,
        visualPageTop: visual.pageTop,
        visualScale: visual.scale
      };
    })()
""".trimIndent()
