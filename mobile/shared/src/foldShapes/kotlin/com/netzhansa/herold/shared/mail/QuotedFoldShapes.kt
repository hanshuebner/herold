package com.netzhansa.herold.shared.mail

/**
 * The seven quoted-history shapes both clients are measured against,
 * with the bodies the Suite's end-to-end spec delivers
 * (`web/apps/suite/tests/e2e-live/quoted-history-fold.spec.ts`), byte
 * for byte (issue #456).
 *
 * The fold decides which part of a reply is the sender's own writing
 * and which is the correspondence it answers. Four rewrites of that
 * decision each shipped a body the rule before it rendered correctly,
 * so the shapes here are the ones those defects were reported on, and
 * every one of them is measured again with an unrelated paragraph
 * ahead of it, after it, and at both ends: a corpus that always begins
 * at the structure under test is what let three of those four through.
 *
 * The table is compiled into two places: the host-JVM check that reads
 * the sanitiser's output (`QuotedFoldShapesTest`) and the instrumented
 * check that reads what the reader sees on a device
 * (`QuotedHistoryFoldAcceptanceTest`). One table, so the two cannot
 * come to disagree on what a shape is.
 *
 * What each shape is:
 *
 *  - s1 a Thunderbird reply written into the citation-prefix div: the
 *    reply stays visible, the attribution and the quote fold.
 *  - s2 the same shape with an attribution the heuristic does not
 *    know: the whole div stays visible, the quote alone folds.
 *  - s3 a passed-over citation-prefix div over a quote holding a
 *    reply of its own: that reply stays inside the fold.
 *  - s4 a quoted top-post, which is not lifted out because the
 *    sender's text precedes the quote elsewhere in the body.
 *  - s5 a reply written below the quote, outside the container the
 *    citation sits in: nothing folds at all.
 *  - s6 the sender's own line in the shape of an attribution, ahead
 *    of a genuine citation div: the reply below it stays visible.
 *  - s7 the correspondent's own paragraph between a real attribution
 *    and the quote it introduces: it stays inside the fold.
 */
object QuotedFoldShapes {

    /** One assertable run of text in a fixture's rendered body. */
    data class Marker(
        val text: String,
        /** True for text the fold hides when a fold happens. */
        val foldsAway: Boolean,
    )

    data class Shape(
        val id: String,
        val html: String,
        val markers: List<Marker>,
        /** False for the shape that never folds, however it is wrapped. */
        val foldsWhenUnwrapped: Boolean,
    )

    /** Where the unrelated paragraphs stand, if anywhere. */
    enum class Wrap(val id: String) {
        PLAIN("plain"),
        LEADING("leading"),
        TRAILING("trailing"),
        BOTH("both"),
    }

    /** One shape in one wrap: the body to deliver and what it owes. */
    data class Fixture(
        val id: String,
        val shape: Shape,
        val wrap: Wrap,
        val html: String,
        val markers: List<Marker>,
        val folds: Boolean,
    )

    const val LEAD = "<p>Unrelated leading paragraph.</p>\n"
    const val TAIL = "\n<p>Unrelated trailing paragraph.</p>"

    val shapes: List<Shape> = listOf(
        Shape(
            id = "s1-thunderbird-reply",
            foldsWhenUnwrapped = true,
            html =
                "<div class=\"moz-cite-prefix\">Hallo Jane,<br><br>das passt mir gut.<br>" +
                "<br>Am 20.09.26 um 14:12 schrieb " +
                "<a class=\"moz-txt-link-abbreviated\" href=\"mailto:jane@example.test\">jane@example.test" +
                "</a>:<br></div>\n" +
                "<blockquote type=\"cite\" cite=\"mid:abc@example.test\">Original quoted text." +
                "</blockquote>",
            markers = listOf(
                Marker("Hallo Jane,", foldsAway = false),
                Marker("das passt mir gut.", foldsAway = false),
                Marker("Am 20.09.26 um 14:12 schrieb", foldsAway = true),
                Marker("Original quoted text.", foldsAway = true),
            ),
        ),
        Shape(
            id = "s2-unrecognised-attribution",
            foldsWhenUnwrapped = true,
            html =
                "<div class=\"moz-cite-prefix\">Hallo Jane,<br><br>das passt mir gut.<br>" +
                "<br>Op 20-09-26 om 14:12 schreef " +
                "<a class=\"moz-txt-link-abbreviated\" href=\"mailto:jane@example.test\">jane@example.test" +
                "</a>:<br></div>\n" +
                "<blockquote type=\"cite\" cite=\"mid:abc@example.test\">Original quoted text." +
                "</blockquote>",
            markers = listOf(
                Marker("Hallo Jane,", foldsAway = false),
                Marker("das passt mir gut.", foldsAway = false),
                Marker("Op 20-09-26 om 14:12 schreef", foldsAway = false),
                Marker("Original quoted text.", foldsAway = true),
            ),
        ),
        Shape(
            id = "s3-compound-leak",
            foldsWhenUnwrapped = true,
            html =
                "<div class=\"moz-cite-prefix\">Hallo Jane,<br><br>das passt mir gut.<br>" +
                "<br>Op 20-09-26 om 14:12 schreef " +
                "<a class=\"moz-txt-link-abbreviated\" href=\"mailto:jane@example.test\">jane@example.test" +
                "</a>:<br></div>\n<blockquote type=\"cite\">" +
                "<div>Older reply text that should stay hidden behind the fold.</div>" +
                "<div>Am 10.09.26 um 18:21 schrieb John Doe:<br>" +
                "<blockquote type=\"cite\">Original original text.</blockquote></div>" +
                "</blockquote>",
            markers = listOf(
                Marker("Hallo Jane,", foldsAway = false),
                Marker("das passt mir gut.", foldsAway = false),
                Marker("Op 20-09-26 om 14:12 schreef", foldsAway = false),
                Marker("Older reply text that should stay hidden behind the fold.", foldsAway = true),
                Marker("Original original text.", foldsAway = true),
            ),
        ),
        Shape(
            id = "s4-quoted-top-post",
            foldsWhenUnwrapped = true,
            html =
                "<p>F5</p><p>Mit freundlichen Gruessen</p>\n<div>" +
                "<p>Le 15 septembre 2026 a 18:21, Alice a ecrit:</p>\n" +
                "<blockquote type=\"cite\"><p>Q5i</p>\n" +
                "<div class=\"moz-cite-prefix\">Am 14.09.26 um 08:00 schrieb bob@example.test:" +
                "<br></div>\n<blockquote type=\"cite\">Q5</blockquote></blockquote></div>\n",
            markers = listOf(
                Marker("F5", foldsAway = false),
                Marker("Mit freundlichen Gruessen", foldsAway = false),
                Marker("Le 15 septembre 2026 a 18:21, Alice a ecrit:", foldsAway = true),
                Marker("Q5i", foldsAway = true),
                Marker("Am 14.09.26 um 08:00 schrieb bob@example.test:", foldsAway = true),
                Marker("Q5", foldsAway = true),
            ),
        ),
        Shape(
            id = "s5-bottom-posted-veto",
            foldsWhenUnwrapped = false,
            html =
                "<div class=\"moz-forward-container\">\n" +
                "<div class=\"moz-cite-prefix\">Am 15.09.26 um 18:21 schrieb Alice:<br></div>\n" +
                "<blockquote type=\"cite\">Q9</blockquote></div>\n<p>F9 written below the quote" +
                "</p>\n",
            markers = listOf(
                Marker("Am 15.09.26 um 18:21 schrieb Alice:", foldsAway = false),
                Marker("Q9", foldsAway = false),
                Marker("F9 written below the quote", foldsAway = false),
            ),
        ),
        Shape(
            id = "s6-grandmother-collision",
            foldsWhenUnwrapped = true,
            html =
                "<p>On the anniversary, my grandmother always wrote:</p>\n" +
                "<div class=\"moz-cite-prefix\">Hallo Jane,<br><br>das passt mir gut.<br>" +
                "<br>Am 20.09.26 um 14:12 schrieb " +
                "<a class=\"moz-txt-link-abbreviated\" href=\"mailto:jane@example.test\">jane@example.test" +
                "</a>:<br></div>\n<blockquote type=\"cite\">Original quoted text.</blockquote>",
            markers = listOf(
                Marker("On the anniversary, my grandmother always wrote:", foldsAway = false),
                Marker("Hallo Jane,", foldsAway = false),
                Marker("das passt mir gut.", foldsAway = false),
                Marker("Am 20.09.26 um 14:12 schrieb", foldsAway = true),
                Marker("Original quoted text.", foldsAway = true),
            ),
        ),
        Shape(
            id = "s7-correspondent-own-paragraph",
            foldsWhenUnwrapped = true,
            html =
                "<p>On Mon, 15 Sep 2026, Alice wrote:</p><p>Prose of my own in between.</p>" +
                "<blockquote type=\"cite\"><p>What Alice wrote above her own quote.</p>" +
                "<div class=\"moz-cite-prefix\">Am 14.09.26 um 08:00 schrieb bob@example.test:" +
                "<br></div><blockquote type=\"cite\">The oldest message.</blockquote>" +
                "</blockquote>",
            markers = listOf(
                Marker("On Mon, 15 Sep 2026, Alice wrote:", foldsAway = false),
                Marker("Prose of my own in between.", foldsAway = false),
                Marker("What Alice wrote above her own quote.", foldsAway = true),
                Marker("Am 14.09.26 um 08:00 schrieb bob@example.test:", foldsAway = true),
                Marker("The oldest message.", foldsAway = true),
            ),
        ),
    )

    /**
     * Every shape in every wrap: twenty-eight bodies.
     */
    fun fixtures(): List<Fixture> = shapes.flatMap { shape ->
        Wrap.entries.map { wrap -> fixture(shape, wrap) }
    }

    fun fixture(shape: Shape, wrap: Wrap): Fixture = Fixture(
        id = "${shape.id}-${wrap.id}",
        shape = shape,
        wrap = wrap,
        html = wrapped(shape.html, wrap),
        markers = shape.markers + wrapMarkers(wrap),
        folds = foldsWhenWrapped(shape, wrap),
    )

    fun wrapped(html: String, wrap: Wrap): String = when (wrap) {
        Wrap.PLAIN -> html
        Wrap.LEADING -> LEAD + html
        Wrap.TRAILING -> html + TAIL
        Wrap.BOTH -> LEAD + html + TAIL
    }

    /**
     * Whether a fold is owed at all.
     *
     * A paragraph after the quote is the reader answering under it,
     * and the quote is then the context that answer stands on: the
     * fold is held back for every shape, and the body renders whole.
     * That rule is the fold's own, from the pass that introduced it
     * (mobile e95032e5, ported from the Suite's #32 and #49), and it
     * is what the Suite's spec asserts for the same bodies. A
     * paragraph ahead of the quote changes nothing: what stands above
     * a reply is the sender's either way.
     */
    fun foldsWhenWrapped(shape: Shape, wrap: Wrap): Boolean =
        shape.foldsWhenUnwrapped && (wrap == Wrap.PLAIN || wrap == Wrap.LEADING)

    private fun wrapMarkers(wrap: Wrap): List<Marker> = buildList {
        if (wrap == Wrap.LEADING || wrap == Wrap.BOTH) {
            add(Marker("Unrelated leading paragraph.", foldsAway = false))
        }
        if (wrap == Wrap.TRAILING || wrap == Wrap.BOTH) {
            add(Marker("Unrelated trailing paragraph.", foldsAway = false))
        }
    }
}
