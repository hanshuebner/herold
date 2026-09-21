package com.netzhansa.herold.shared.mail

import kotlin.test.Test
import kotlin.test.assertTrue

/**
 * Where the two candidate rules for "may this region hold the
 * sender's own text" answer differently (issue #432).
 *
 * The rule the fold uses is relational: it reads what stands ahead of
 * the region, at its own level and at every level above, and a
 * citation there hands the region to the message that citation
 * introduces. The rule under consideration is intrinsic: a region is
 * the quoted message's when it, or something it sits in, is a quote
 * container by its own tag or class - a `blockquote` or a client's
 * own container marking - whatever surrounds it.
 *
 * Both are run over every body the fold is measured against and over
 * bodies generated to the shapes where they have room to differ. The
 * disagreements are the whole output: where they answer alike the
 * choice between them is a matter of taste, and where they do not one
 * of them renders the correspondent's words as the sender's or hides
 * the sender's own writing.
 */
class QuotedFoldPredicateComparisonTest {

    private data class Answer(
        val name: String,
        val html: String,
        val relational: Boolean,
        val intrinsic: Boolean,
        /** The same rule read as ancestors only, the candidate aside. */
        val intrinsicAncestorsOnly: Boolean,
        /** Whether the answer changes what the reader sees. */
        val consequential: Boolean,
    )

    @Test
    fun theRulesAreComparedOnEveryBody() {
        val bodies = corpusBodies() + clientShapes() + generatedBodies()
        val answers = bodies.mapNotNull { (name, html) -> answerFor(name, html) }
        val differing = answers.filter { it.relational != it.intrinsic }
        val consequential = differing.filter { it.consequential }

        println(
            "COMPARED\tbodies=${bodies.size}\twithAQuotedRegion=${answers.size}" +
                "\tdiffering=${differing.size}\tconsequential=${consequential.size}",
        )
        consequential.forEach {
            println(
                "DIFFERS\t${it.name}\trelational=${it.relational}\tintrinsic=${it.intrinsic}" +
                    "\tintrinsicAncestorsOnly=${it.intrinsicAncestorsOnly}\t${it.html}",
            )
        }
        answers.filter { it.relational != it.intrinsicAncestorsOnly && it.consequential }.forEach {
            println(
                "DIFFERSANCESTORSONLY\t${it.name}\trelational=${it.relational}" +
                    "\tintrinsicAncestorsOnly=${it.intrinsicAncestorsOnly}\t${it.html}",
            )
        }
        differing.map { it.name.substringBefore('#') }.toSet().sorted().forEach { kind ->
            println(
                "DIFFERKIND\t$kind\tall=${differing.count { it.name.startsWith(kind) }}" +
                    "\tconsequential=${consequential.count { it.name.startsWith(kind) }}",
            )
        }

        assertTrue(bodies.size >= 400, "too few bodies to call the comparison a measurement: ${bodies.size}")
        assertTrue(differing.isNotEmpty(), "the two rules answered alike on every body, which they do not")
        // Every disagreement runs the same way round: the intrinsic
        // rule is the stricter of the two, folding leading content the
        // relational rule leaves with the sender. A body where the
        // intrinsic rule lifts and the relational one does not would
        // be a shape neither has been reasoned about.
        val reversed = differing.filter { it.intrinsic && !it.relational }
        assertTrue(
            reversed.isEmpty(),
            "the intrinsic rule lifted where the relational one did not:\n" +
                reversed.joinToString("\n") { "${it.name}: ${it.html}" },
        )
    }

    private fun answerFor(name: String, html: String): Answer? {
        val root = HtmlDom.parse(html)
        val candidate = QuotedHtml.firstQuotedRegion(root, emptyList()) ?: return null
        return Answer(
            name = name,
            html = html,
            relational = !QuotedHtml.isBeneathACitation(candidate),
            intrinsic = !isInsideAQuoteContainer(candidate),
            intrinsicAncestorsOnly = !isInsideAQuoteContainer(candidate, countTheCandidate = false),
            consequential = QuotedHtml.liftableLeadingContent(candidate) > 0,
        )
    }

    /**
     * The intrinsic rule: a region is the quoted message's when it, or
     * an element it sits in, is a quote container by its own tag or
     * class. A citation-prefix div is not one - it names the
     * attribution line rather than holding a message.
     */
    private fun isInsideAQuoteContainer(candidate: HtmlElement, countTheCandidate: Boolean = true): Boolean {
        var element: HtmlElement? = if (countTheCandidate) candidate else candidate.parent
        while (element != null) {
            if (element.name == "blockquote" || containerClass.containsMatchIn(element.classes)) return true
            element = element.parent
        }
        return false
    }

    // ---- the bodies ------------------------------------------------------

    private fun corpusBodies(): List<Pair<String, String>> =
        QuotedFoldCorpus.all().map { "corpus-${it.name}" to it.html }

    /** The client shapes the named fixtures cover, and the defects. */
    private fun clientShapes(): List<Pair<String, String>> = listOf(
        "shape-trailing-blockquote" to
            "<p>My reply.</p><blockquote>Original message body.</blockquote>",
        "shape-gmail-quote-div" to
            "<p>My reply.</p><div class=\"gmail_quote_attribution\">On Mon, Alice wrote:</div>" +
            "<div class=\"gmail_quote\">Original quoted text</div>",
        "shape-bottom-posted" to
            "<blockquote><p>On Mon Alice wrote: original message text</p></blockquote>" +
            "<div><p>My bottom-posted reply.</p></div>",
        "shape-interleaved" to
            "<p>First reply.</p><blockquote>First quoted block.</blockquote>" +
            "<p>Second reply.</p><blockquote>Second quoted block.</blockquote>",
        "shape-quote-only" to "<blockquote>Only quoted text, no reply at all.</blockquote>",
        "shape-introducer" to
            "<p>My reply.</p><p>On Mon, 13 Jul 2026, Alice wrote:</p><blockquote>Original.</blockquote>",
        "shape-signature" to
            "<p>My reply.</p><blockquote>Original.</blockquote><p></p><p>-- </p><p>Alice</p>",
        "shape-gmail-cursor-in-quote" to
            "<blockquote class=\"gmail_quote\"><div>Sounds good, let us proceed on Monday.</div>" +
            "<div class=\"gmail_attr\">On Mon, Alice wrote:<br></div>" +
            "<blockquote class=\"gmail_quote\"><div>Original quoted text.</div></blockquote></blockquote>",
        "shape-thunderbird-bundle" to
            "<p>Hallo Herr Mustermann,</p><div>Am 10.09.26 um 18:21 schrieb Jane Doe:<br>" +
            "<blockquote type=\"cite\">Original original text.</blockquote></div>",
        "shape-forward-container" to
            "<p>Vielen Dank.</p><div class=\"moz-forward-container\">" +
            "<div class=\"moz-cite-prefix\">Am 10.09.26 um 18:21 schrieb Jane Doe:<br></div>" +
            "<blockquote type=\"cite\">Original original text.</blockquote></div>",
        "shape-cite-prefix-siblings" to
            "<p>Mit freundlichen Gruessen</p>" +
            "<div class=\"moz-cite-prefix\">Am 10.09.26 um 18:21 schrieb Jane Doe:<br></div>" +
            "<blockquote type=\"cite\">Original original text.</blockquote>",
        "shape-plain-quote-no-marker" to
            "<p>My reply.</p><blockquote><div>Line one.</div><div>Line two.</div></blockquote>",
        // The five shapes that were defects.
        "defect-thunderbird-reply-in-cite-prefix" to THUNDERBIRD_REPLY,
        "defect-thunderbird-reply-with-a-line-above" to "<p>Fresh sender line</p>$THUNDERBIRD_REPLY",
        "defect-quoted-lead-beneath-a-citation" to
            "<p>My reply.</p><p>Mit freundlichen Gruessen</p>" +
            "<div><p>Le 15 septembre 2026 a 18:21, Alice a ecrit:</p>$CITED_QUOTE</div>",
        "defect-bottom-post-past-a-wrapper" to
            "<div class=\"moz-forward-container\">" +
            "<div class=\"moz-cite-prefix\">Am 15.09.26 um 18:21 schrieb Alice:<br></div>" +
            "<blockquote type=\"cite\">Q9</blockquote></div><p>Written below the quote.</p>",
        "defect-sender-line-reads-as-an-attribution" to
            "<p>Am 19.09.26 um 09:00 schrieb Bob:</p>$THUNDERBIRD_REPLY",
        // The three shapes the Suite's own table names, where the two
        // rules part company on real mail rather than on a
        // construction.
        "crossclient-quoted-lead-in-a-plain-wrapper" to
            "<p>My reply.</p><div><p>On Mon, 15 Sep 2026, Alice wrote:</p>$CITED_QUOTE</div>",
        "crossclient-quoted-lead-in-a-plain-wrapper-german" to
            "<p>Danke.</p><div><p>Am 15.09.26 um 18:21 schrieb Alice:</p>$CITED_QUOTE</div>",
        "crossclient-attribution-then-wrapped-cite-prefix" to
            "<p>On Mon, 15 Sep 2026, Alice wrote:</p><div>$THUNDERBIRD_REPLY</div>",
        "crossclient-forwarding-note-in-a-forward-container" to
            "<div class=\"moz-forward-container\">" +
            "<div class=\"moz-cite-prefix\">Weitergeleitet, weil es dich betrifft.<br><br>" +
            "Am 15.09.26 um 18:21 schrieb Alice:<br></div>" +
            "<blockquote type=\"cite\">The forwarded message.</blockquote></div>",
        "crossclient-prose-matching-the-attribution-regex" to
            "<p>On the anniversary, my grandmother always wrote:</p>$THUNDERBIRD_REPLY",
    )

    /**
     * Bodies in the six shapes the two rules have room to differ on:
     * a citation ahead that introduces nothing, a container with no
     * citation, a citation introducing something that is not a
     * container, benign nodes between the two, containers inside
     * containers, and a container marked by a class alone.
     */
    private fun generatedBodies(): List<Pair<String, String>> {
        val bodies = mutableListOf<Pair<String, String>>()
        val citations = listOf(
            "english" to "<p>On Mon, 15 Sep 2026, Alice wrote:</p>",
            "german" to "<p>Am 15.09.26 um 18:21 schrieb Alice:</p>",
            "nameFirst" to "<p>Alice schrieb am 15.09.26 um 18:21:</p>",
            "citeDiv" to "<div class=\"moz-cite-prefix\">Am 15.09.26 um 18:21 schrieb Alice:<br></div>",
            "bundled" to "<div>Am 15.09.26 um 18:21 schrieb Alice:<br></div>",
        )
        val gaps = listOf(
            "none" to "",
            "comment" to "<!-- a comment -->",
            "span" to "<span></span>",
            "whitespace" to "\n   ",
            "br" to "<br>",
            "all" to "<!-- c -->\n <span></span> <br>  ",
        )
        val containers = listOf(
            "blockquote" to { inner: String -> "<blockquote type=\"cite\">$inner</blockquote>" },
            "gmailQuote" to { inner: String -> "<div class=\"gmail_quote\">$inner</div>" },
            "yahooQuoted" to { inner: String -> "<div class=\"yahoo_quoted\">$inner</div>" },
            "gmailBlockquote" to { inner: String -> "<blockquote class=\"gmail_quote\">$inner</blockquote>" },
        )
        val inner = "<p>Q-lead</p><div class=\"moz-cite-prefix\">Am 14.09.26 um 08:00 schrieb Bob:<br></div>" +
            "<blockquote type=\"cite\">Q-oldest</blockquote>"

        containers.forEach { (containerName, container) ->
            citations.forEach { (citationName, citation) ->
                gaps.forEach { (gapName, gap) ->
                    // 4: benign nodes between a citation and what it introduces.
                    bodies += "citation-introducing-a-container#$containerName-$citationName-$gapName" to
                        "<p>My reply.</p>$citation$gap${container(inner)}"
                    // 1: a citation ahead that introduces something else,
                    // with the sender's prose between it and the candidate.
                    bodies += "citation-that-introduces-nothing#$containerName-$citationName-$gapName" to
                        "$citation<p>Prose of my own in between.</p>$gap${container(inner)}"
                    // 5: containers inside containers.
                    bodies += "container-in-a-container#$containerName-$citationName-$gapName" to
                        "<p>My reply.</p>$citation$gap${container(container(inner))}"
                }
                // 3: a citation introducing what is not a container.
                bodies += "citation-introducing-a-prefix#$containerName-$citationName" to
                    "<p>My reply.</p>$citation" +
                    "<div class=\"moz-cite-prefix\">Hallo,<br><br>my answer.<br><br>" +
                    "Am 20.09.26 um 14:12 schrieb <a href=\"mailto:a@x.test\">a@x.test</a>:<br></div>" +
                    container("Q-oldest")
            }
            // 2 and 6: a container with no citation anywhere ahead of it,
            // with and without the sender's prose above.
            listOf("bare" to "", "afterProse" to "<p>My reply.</p>").forEach { (lead, prose) ->
                bodies += "container-without-a-citation#$containerName-$lead" to "$prose${container(inner)}"
                bodies += "container-without-a-citation#$containerName-$lead-nested" to
                    "$prose${container(container(inner))}"
            }
        }

        // Every generated body again with a paragraph at either end and
        // at both, since a rule that reads what surrounds a region has
        // to answer the same way when the body carries more around it.
        return bodies + bodies.flatMap { (name, html) ->
            listOf(
                "$name-ahead" to "<p>A line above.</p>$html",
                "$name-after" to "$html<p>A line below.</p>",
                "$name-around" to "<p>A line above.</p>$html<p>A line below.</p>",
            )
        }
    }

    private companion object {
        val containerClass = Regex("gmail_quote|yahoo_quoted|moz-forward-container", RegexOption.IGNORE_CASE)

        const val THUNDERBIRD_REPLY =
            "<div class=\"moz-cite-prefix\">Hallo Jane,<br><br>das passt mir gut.<br><br>" +
                "Am 20.09.26 um 14:12 schrieb " +
                "<a href=\"mailto:jane@example.test\">jane@example.test</a>:<br></div>" +
                "<blockquote type=\"cite\">Original quoted text.</blockquote>"

        const val CITED_QUOTE =
            "<blockquote type=\"cite\"><p>What Alice wrote above her own quote.</p>" +
                "<div class=\"moz-cite-prefix\">Am 14.09.26 um 08:00 schrieb bob@example.test:<br></div>" +
                "<blockquote type=\"cite\">The oldest message.</blockquote></blockquote>"
    }
}
