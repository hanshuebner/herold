package com.netzhansa.herold.android

import android.graphics.BitmapFactory
import android.graphics.Color
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.netzhansa.herold.android.media.ImageScaling
import com.netzhansa.herold.android.ui.thread.MessageBodyWebView
import com.netzhansa.herold.shared.mail.HtmlSanitizer
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.FixMethodOrder
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.MethodSorters

/**
 * What an inline image with an alpha channel looks like in the reading
 * pane (issue #445).
 *
 * The reported message carries a 1875 by 288 PNG of colour type 6 whose
 * background is transparent over white, in a cell declaring
 * `background-color:#F5F5F5` inside a container declaring white. The
 * image is wider than the pane decodes at, so it goes through
 * [ImageScaling.forDisplay], and the pane drew it on a black rectangle.
 *
 * The body surface is driven here on its own, with the resolver the
 * pane uses and no network, so the assertions are about the image and
 * not about a server. The checks read the pixels the device put on
 * screen: the transparent half has to be the colour underneath it,
 * which is the sender's cell where the sender declared one and the
 * pane's own background where they did not. Both themes are driven, so
 * flattening the image onto white fails the dark check as flattening it
 * onto black fails the light one.
 *
 * The class needs no account and no dev instance, and leaves nothing
 * behind, so it runs in any position of the suite and twice over
 * (issue #414).
 */
@RunWith(AndroidJUnit4::class)
@FixMethodOrder(MethodSorters.NAME_ASCENDING)
class InlineImageAlphaAcceptanceTest {

    @get:Rule
    val compose = createComposeRule()

    private val instrumentation get() = InstrumentationRegistry.getInstrumentation()

    /** What the pane decodes an inline image at: the column, not the image. */
    private val displayWidthPx: Int
        get() = instrumentation.targetContext.resources.displayMetrics.widthPixels

    /** The logo over the cell the sender coloured, in the light theme. */
    @Test
    fun t10_aTransparentLogoKeepsItsTransparencyInTheLightTheme() {
        rendersOverWhatIsUnderIt(darkTheme = false, shot = "m4-inline-alpha-light")
    }

    /** The same message in the dark theme, where white would be as wrong as black. */
    @Test
    fun t20_aTransparentLogoKeepsItsTransparencyInTheDarkTheme() {
        rendersOverWhatIsUnderIt(darkTheme = true, shot = "m4-inline-alpha-dark")
    }

    /**
     * The decision the pane makes about the bytes it serves: an image
     * with an alpha channel comes back in a format that carries one, at
     * the type the WebView is told, and still bounded by the display
     * edge.
     */
    @Test
    fun t30_theDisplayPathKeepsTheAlphaChannelAndReportsItsType() {
        val source = transparentBannerPng()
        assertEquals("the fixture is not a colour-type 6 PNG", 6, pngColourType(source))
        assertTrue(
            "the fixture must be wider than the display bound, saw ${ImageScaling.dimensions(source)}",
            ImageScaling.dimensions(source)!!.first > displayWidthPx,
        )

        val (type, bytes) = ImageScaling.forDisplay("image/png", source, displayWidthPx)
        val decoded = BitmapFactory.decodeByteArray(bytes, 0, bytes.size)!!

        assertEquals("the display path re-encoded the image to another format", "image/png", type)
        assertTrue("the scaled image lost its alpha channel", decoded.hasAlpha())
        assertEquals(
            "a pixel of the transparent half came back opaque",
            0,
            Color.alpha(decoded.getPixel(decoded.width / 4, decoded.height / 2)),
        )
        assertTrue(
            "the display bound no longer holds: ${decoded.width} by ${decoded.height}",
            maxOf(decoded.width, decoded.height) <= displayWidthPx,
        )
        assertTrue(
            "the scaled image is no smaller than the original: ${bytes.size} of ${source.size}",
            bytes.size < source.size,
        )
    }

    // ---- the flow --------------------------------------------------------

    private fun rendersOverWhatIsUnderIt(darkTheme: Boolean, shot: String) {
        val source = transparentBannerPng()
        val served = mutableMapOf<String, String>()
        val resolve: (String) -> Pair<String, ByteArray>? = { cid ->
            if (cid == LOGO_CID || cid == MARK_CID) {
                ImageScaling.forDisplay("image/png", source, displayWidthPx).also { served[cid] = it.first }
            } else {
                null
            }
        }
        val html = HtmlSanitizer.document(
            HtmlSanitizer.sanitize(BODY, fitToWidthCssPx = CONTENT_CSS_PX).html,
            darkTheme = darkTheme,
            contentWidthCssPx = CONTENT_CSS_PX,
        )

        compose.setContent {
            MessageBodyWebView(
                html = html,
                imageSources = true,
                resolveInlineImage = resolve,
                resolveRemoteImage = { _ -> null },
                onLink = {},
                modifier = Modifier.fillMaxSize().testTag(BODY_TAG),
            )
        }

        val logo = awaitBodyImage(LOGO_ALT)
        val mark = awaitBodyImage(MARK_ALT)
        compose.captureScreen(shot)
        val screen = deviceScreen()

        // The sender's cell shows through the logo's transparent half in
        // either theme; the pane's own background shows through the
        // second image, which sits in no coloured cell.
        val theme = if (darkTheme) "dark theme" else "light theme"
        val pane = if (darkTheme) Color.rgb(0x1b, 0x1b, 0x1b) else Color.WHITE
        assertShowsThrough(screen, logo, Color.rgb(0xf5, 0xf5, 0xf5), "the sender's cell", theme)
        assertShowsThrough(screen, mark, pane, "the pane's background", theme)
        assertEquals("the WebView was told the wrong type", setOf("image/png"), served.values.toSet())
    }

    private companion object {
        const val BODY_TAG = "message-body-under-test"

        const val LOGO_CID = "logo@example.invalid"
        const val MARK_CID = "mark@example.invalid"
        const val LOGO_ALT = "logo"
        const val MARK_ALT = "mark"

        /** How wide the card gives the body. */
        const val CONTENT_CSS_PX = 336

        /**
         * The sender's markup: the logo in a cell declaring a light grey
         * inside a white container, and a second copy of the same image
         * where the sender declared nothing, so the pane's own
         * background is what is behind it.
         */
        val BODY = """
            <html><body>
              <div style="background-color:white;padding:0">
                <div style="background-color:#F5F5F5;padding:0">
                  <img src="cid:$LOGO_CID" alt="$LOGO_ALT">
                </div>
              </div>
              <img src="cid:$MARK_CID" alt="$MARK_ALT">
              <p>Below the images.</p>
            </body></html>
        """.trimIndent()
    }
}
