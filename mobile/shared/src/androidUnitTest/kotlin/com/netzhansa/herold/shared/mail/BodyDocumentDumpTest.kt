package com.netzhansa.herold.shared.mail

import java.io.File
import kotlin.test.Test

/**
 * Writes the document the reading pane renders to where a browser can
 * open it, so the layout the sanitiser produces can be measured against
 * the same engine the WebView runs (issue #430). The dump is skipped
 * unless HEROLD_BODY_DUMP names a directory, so CI never writes files.
 */
class BodyDocumentDumpTest {

    @Test
    fun dumpsTheDesktopNewsletter() {
        val directory = System.getenv("HEROLD_BODY_DUMP")?.takeIf { it.isNotBlank() } ?: return
        val card = System.getenv("HEROLD_BODY_CARD")?.toIntOrNull() ?: 411
        val content = HtmlSanitizer.contentWidthCssPx(card)
        File(directory).mkdirs()
        listOf("newsletter" to NEWSLETTER, "nowrap" to NOWRAP).forEach { (name, fixture) ->
            val sanitized = HtmlSanitizer.sanitize(fixture, fitToWidthCssPx = content)
            val document = HtmlSanitizer.document(sanitized.html, darkTheme = false, contentWidthCssPx = content)
            File(directory, "$name.html").writeText(document + PROBE)
            File(directory, "$name.txt").writeText(
                "content=$content minimum=${sanitized.minimumWidthCssPx}\n",
            )
        }
    }

    private companion object {
        /**
         * Reports what runs past the card. The probe is appended to the
         * dump only, never to a document the app renders.
         */
        val PROBE = """
            <script>
            window.addEventListener('load', function () {
              var w = document.documentElement.clientWidth;
              var out = ['viewport=' + w,
                         'scrollWidth=' + document.documentElement.scrollWidth,
                         'bodyScroll=' + document.body.scrollWidth];
              var box = document.querySelector('.herold-body');
              out.push('bodyBoxScroll=' + box.scrollWidth + ' clientWidth=' + box.clientWidth);
              document.querySelectorAll('.herold-body *').forEach(function (el) {
                var r = el.getBoundingClientRect();
                if (r.right > w + 0.5) {
                  out.push('OVER ' + el.tagName +
                           (el.getAttribute('width') ? '[width=' + el.getAttribute('width') + ']' : '') +
                           (el.getAttribute('style') ? '[style=' + el.getAttribute('style') + ']' : '') +
                           ' left=' + Math.round(r.left) + ' right=' + Math.round(r.right));
                }
              });
              var pre = document.createElement('pre');
              pre.id = 'herold-probe';
              pre.textContent = out.join('\n');
              document.body.appendChild(pre);
            });
            </script>
        """.trimIndent()

        val NEWSLETTER = "<html><body>" +
            "<div style=\"padding:20px;background:#eeeeee\">" +
            "<table width=\"575\" align=\"center\" style=\"width:575px\" cellpadding=\"0\" cellspacing=\"0\">" +
            "<tr><td><img src=\"banner.png\" width=\"555\" height=\"40\" alt=\"banner\"></td></tr>" +
            "<tr><td><p>The newsletter as the sender laid it out.</p></td></tr>" +
            "<tr><td>" +
            "<table width=\"555\" style=\"width:555px\" cellpadding=\"8\"><tr>" +
            "<td width=\"277\" style=\"width:277px\">Left column of the newsletter, as the " +
            "template writes it for a desktop reading pane." +
            "<img src=\"left.png\" width=\"261\" height=\"40\" alt=\"left\"></td>" +
            "<td width=\"278\" style=\"width:278px\">Right column of the newsletter, which is " +
            "the part that ran off the screen." +
            "<img src=\"right.png\" width=\"262\" height=\"40\" alt=\"right\"></td>" +
            "</tr></table></td></tr>" +
            "<tr><td><img src=\"footer.png\" width=\"555\" height=\"40\" alt=\"footer\">" +
            "<p>https://newsletter.example.com/a/very/long/tracking/url/that/has/no/spaces/in/it/at/all</p>" +
            "</td></tr>" +
            "</table></div></body></html>"

        val NOWRAP = "<html><body><div style=\"padding:20px\">" +
            "<table width=\"575\" align=\"center\" style=\"width:575px\">" +
            "<tr><td><p>A layout that cannot be narrowed.</p>" +
            "<table><tr style=\"white-space:nowrap\">" +
            (1..40).joinToString("") { "<td>column $it</td>" } +
            "</tr></table></td></tr></table></div></body></html>"
    }
}
