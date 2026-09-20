package com.netzhansa.herold.shared.mail

import kotlin.test.Test
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * Two corpora of reply bodies, built differently on purpose, measured
 * against one invariant: what the sender wrote renders outside the
 * fold, and what the quoted message carries renders inside it
 * (issue #432).
 *
 * The named fixtures in `QuotedHistoryTest` are the shapes a client is
 * known to emit. These are combinations of them - a wrapper around a
 * bundle around a nested quote - because the defects this pass has
 * produced twice came from a shape reached through another shape, not
 * from either alone. The first corpus enumerates the combinations by
 * hand, in shape order; the second draws them from a seeded sequence in
 * a different order and with different renderings of the same parts, so
 * one generator's blind spot is not the whole measurement.
 *
 * Every case carries what it is made of, so the assertion is the
 * contract rather than a golden string: a marker the sender wrote must
 * stand ahead of the chip, a marker the quoted message carries must sit
 * behind it, and a body with fresh text below the quote folds nothing
 * at all.
 */
class QuotedFoldCorpusTest {

    /** One generated body and what the fold owes it. */
    private data class FoldCase(
        val name: String,
        val html: String,
        val fresh: List<String>,
        val quoted: List<String>,
        val folds: Boolean,
    )

    @Test
    fun theEnumeratedCorpusFoldsTheQuoteAndOnlyTheQuote() = check(enumeratedCorpus())

    @Test
    fun theSeededCorpusFoldsTheQuoteAndOnlyTheQuote() = check(seededCorpus())

    @Test
    fun theEnumeratedCorpusHoldsWithAParagraphAroundIt() = check(withTextAround(enumeratedCorpus()))

    @Test
    fun theSeededCorpusHoldsWithAParagraphAroundIt() = check(withTextAround(seededCorpus()))

    /**
     * Every body again with an unrelated paragraph in front of it and
     * again with one after it.
     *
     * A fixture written to a client's shape starts at that shape, and
     * a pass that reads the document around the quote rather than the
     * quote itself is measured by nothing in such a set. A note above
     * the reply and a line below it are what a real message carries,
     * and each moves a different rule: what stands ahead of the quote
     * must not change where the citation begins, and what stands after
     * it means the quote is not what the message ends with.
     */
    private fun withTextAround(corpus: List<FoldCase>): List<FoldCase> = corpus.flatMap { case ->
        val ahead = FoldCase(
            name = "${case.name}+ahead",
            html = "<p>A-${case.name}</p>${case.html}",
            fresh = case.fresh + "A-${case.name}",
            quoted = case.quoted,
            folds = case.folds,
        )
        // A signature runs to the end of the message, so a line written
        // after one is part of it rather than something the fold has to
        // leave alone; those bodies carry the leading permutation only.
        if (case.html.contains(SIGNATURE)) {
            listOf(ahead)
        } else {
            listOf(
                ahead,
                FoldCase(
                    name = "${case.name}+after",
                    html = "${case.html}<p>B-${case.name}</p>",
                    fresh = case.fresh + "B-${case.name}",
                    quoted = case.quoted,
                    folds = false,
                ),
            )
        }
    }

    private fun check(corpus: List<FoldCase>) {
        val failures = mutableListOf<String>()
        corpus.forEach { case ->
            val out = HtmlSanitizer.sanitize(case.html, collapseQuotes = true).html
            println(
                "CORPUS\t${case.name}\t${case.html.replace("\n", "\\n")}\t" +
                    out.replace("\n", "\\n"),
            )
            failures += verdicts(case, out)
        }
        assertTrue(corpus.size >= 25, "the corpus is too small to measure anything: ${corpus.size}")
        assertTrue(failures.isEmpty(), failures.joinToString("\n"))
    }

    private fun verdicts(case: FoldCase, out: String): List<String> {
        val problems = mutableListOf<String>()
        val start = out.indexOf(DETAILS)
        val end = out.indexOf("</details>")
        if (!case.folds) {
            if (start >= 0) problems += "${case.name}: folded a body whose fresh text follows the quote"
            (case.fresh + case.quoted).forEach {
                if (!out.contains(it)) problems += "${case.name}: \"$it\" went missing"
            }
            return problems
        }
        if (start < 0) {
            problems += "${case.name}: nothing folded: $out"
            return problems
        }
        case.fresh.forEach {
            val at = out.indexOf(it)
            when {
                at < 0 -> problems += "${case.name}: the sender's \"$it\" went missing"
                at > start -> problems += "${case.name}: the sender's \"$it\" folded away"
            }
        }
        case.quoted.forEach {
            val at = out.indexOf(it)
            when {
                at < 0 -> problems += "${case.name}: the quoted \"$it\" went missing"
                at < start -> problems += "${case.name}: the quoted \"$it\" renders as the sender's own text"
                at > end -> problems += "${case.name}: the quoted \"$it\" fell out of the fold"
            }
        }
        return problems
    }

    // ---- corpus one: the combinations, enumerated in shape order --------

    private fun enumeratedCorpus(): List<FoldCase> {
        val cases = mutableListOf<FoldCase>()
        var n = 0
        fun add(name: String, html: String, fresh: List<String>, quoted: List<String>, folds: Boolean = true) {
            n++
            cases += FoldCase("e$n-$name", html, fresh, quoted, folds)
        }

        val attributions = listOf(
            "plain" to "On Mon, 15 Sep 2026, Alice Example wrote:",
            "german" to "Am 15.09.26 um 18:21 schrieb Alice Example:",
            "nameFirst" to "Alice Example schrieb am 15.09.26 um 18:21:",
        )

        // A top-posted reply, with and without the whitespace a client
        // serialises between block tags, and with and without a
        // signature under the quote.
        attributions.forEach { (label, line) ->
            listOf("" to "tight", "\n" to "spaced").forEach { (gap, spacing) ->
                listOf(false, true).forEach { signed ->
                    val fresh = "F-top-$label-$spacing-$signed"
                    val quote = "Q-top-$label-$spacing-$signed"
                    val sig = if (signed) "<p></p><p>-- </p><p>S-$label-$spacing</p>" else ""
                    add(
                        "top-$label-$spacing${if (signed) "-signed" else ""}",
                        "<p>$fresh</p>$gap<p>$line</p>$gap<blockquote>$quote</blockquote>$sig",
                        listOf(fresh),
                        listOfNotNull(quote, line, if (signed) "S-$label-$spacing" else null),
                    )
                }
            }
        }

        // Thunderbird: the reply and the attribution in one
        // citation-prefix div, the address rendered three ways, over a
        // flat and over a nested quote.
        val addresses = listOf(
            "bare" to "alice@example.test",
            "link" to "<a class=\"moz-txt-link-abbreviated\" href=\"mailto:alice@example.test\">alice@example.test</a>",
            "entity" to "&lt;alice@example.test&gt;",
        )
        addresses.forEach { (label, address) ->
            listOf(false, true).forEach { nested ->
                val fresh = "F-tb-$label-$nested"
                val quote = "Q-tb-$label-$nested"
                val inner = if (nested) {
                    "<p>Q-tb-lead-$label</p>" +
                        "<div class=\"moz-cite-prefix\">Am 14.09.26 um 08:00 schrieb bob@example.test:<br></div>" +
                        "<blockquote type=\"cite\">$quote</blockquote>"
                } else {
                    quote
                }
                add(
                    "thunderbird-$label${if (nested) "-nested" else ""}",
                    "<div class=\"moz-cite-prefix\">Hallo Alice,<br><br>$fresh<br><br>" +
                        "Am 15.09.26 um 18:21 schrieb $address:<br></div>" +
                        "<blockquote type=\"cite\">$inner</blockquote>",
                    listOf(fresh, "Hallo Alice,"),
                    listOfNotNull(quote, if (nested) "Q-tb-lead-$label" else null),
                )
            }
        }

        // A citation-prefix div whose attribution is in no shape the
        // heuristic knows: the reply stays visible and the quote alone
        // folds, nested quote included.
        listOf(false, true).forEach { nested ->
            val fresh = "F-unknown-$nested"
            val quote = "Q-unknown-$nested"
            val inner = if (nested) {
                "<p>Q-unknown-lead-$nested</p><blockquote type=\"cite\">$quote</blockquote>"
            } else {
                quote
            }
            add(
                "unrecognised-attribution${if (nested) "-nested" else ""}",
                "<div class=\"moz-cite-prefix\">$fresh<br><br>15.09.2026 18:21 &gt;&gt; alice@example.test<br></div>" +
                    "<blockquote type=\"cite\">$inner</blockquote>",
                listOf(fresh, "15.09.2026 18:21"),
                listOfNotNull(quote, if (nested) "Q-unknown-lead-$nested" else null),
            )
        }

        // Apple Mail's bundle, Thunderbird's forward container, and
        // Gmail's cursor-in-quote nesting, each over a nested quote.
        add(
            "apple-bundle",
            "<p>F-apple</p><div>Am 15.09.26 um 18:21 schrieb Alice:<br>" +
                "<blockquote type=\"cite\"><p>Q-apple-lead</p>" +
                "<blockquote type=\"cite\">Q-apple</blockquote></blockquote></div>",
            listOf("F-apple"),
            listOf("Q-apple-lead", "Q-apple"),
        )
        add(
            "forward-container",
            "<p>F-forward</p><div class=\"moz-forward-container\">" +
                "<div class=\"moz-cite-prefix\">Am 15.09.26 um 18:21 schrieb Alice:<br></div>" +
                "<blockquote type=\"cite\">Q-forward</blockquote></div>",
            listOf("F-forward"),
            listOf("Q-forward"),
        )
        add(
            "gmail-cursor-in-quote",
            "<blockquote class=\"gmail_quote\"><div>F-gmail</div>" +
                "<div class=\"gmail_attr\">On Mon, 15 Sep 2026, Alice wrote:<br></div>" +
                "<blockquote class=\"gmail_quote\"><div>Q-gmail-lead</div>" +
                "<div class=\"gmail_attr\">On Sun, 14 Sep 2026, Bob wrote:<br></div>" +
                "<blockquote class=\"gmail_quote\">Q-gmail</blockquote></blockquote></blockquote>",
            listOf("F-gmail"),
            listOf("Q-gmail-lead", "Q-gmail"),
        )

        // Nothing folds when the sender wrote below the quote.
        add(
            "bottom-post",
            "<blockquote>Q-bottom</blockquote><p>F-bottom</p>",
            listOf("F-bottom"),
            listOf("Q-bottom"),
            folds = false,
        )
        add(
            "interleaved",
            "<p>F-inter-1</p><blockquote>Q-inter-1</blockquote>" +
                "<p>F-inter-2</p><blockquote>Q-inter-2</blockquote>",
            listOf("F-inter-1", "F-inter-2"),
            listOf("Q-inter-1", "Q-inter-2"),
            folds = false,
        )
        add(
            "bottom-post-under-a-cite-prefix",
            "<div class=\"moz-cite-prefix\">Am 15.09.26 um 18:21 schrieb Alice:<br></div>" +
                "<blockquote type=\"cite\">Q-bottom-tb</blockquote><p>F-bottom-tb</p>",
            listOf("F-bottom-tb"),
            listOf("Q-bottom-tb"),
            folds = false,
        )

        return cases
    }

    // ---- corpus two: the same parts, drawn in a different order ---------

    /**
     * Draws bodies from a seeded sequence: the wrapper chain, where the
     * sender's text sits, how the attribution is written, what the
     * quote is held in, whether the quoted message carries a reply of
     * its own, and what trails the quote are all chosen independently,
     * so the combinations are not the ones the enumeration above walks.
     */
    private fun seededCorpus(): List<FoldCase> {
        val random = Lcg(0x5EEDL)
        return (1..48).map { index -> seededCase(index, random) }
    }

    private fun seededCase(index: Int, random: Lcg): FoldCase {
        val fresh = "F$index"
        val quote = "Q$index"
        val innerQuote = "Q${index}i"

        val attribution = when (random.next(4)) {
            0 -> "On Mon, 15 Sep 2026 at 18:21, Alice &lt;alice@example.test&gt; wrote:"
            1 -> "Am 15.09.26 um 18:21 schrieb <a href=\"mailto:alice@example.test\">alice@example.test</a>:"
            2 -> "Alice Example schrieb am 15.09.26 um 18:21:"
            else -> "Le 15 septembre 2026 a 18:21, Alice a ecrit:"
        }
        val nested = random.next(2) == 1
        val quotedBody = if (nested) {
            "<p>$innerQuote</p>" +
                "<div class=\"moz-cite-prefix\">Am 14.09.26 um 08:00 schrieb bob@example.test:<br></div>" +
                "<blockquote type=\"cite\">$quote</blockquote>"
        } else {
            "<p>$quote</p>"
        }
        val quoted = if (nested) listOf(innerQuote, quote) else listOf(quote)

        val container = when (random.next(3)) {
            0 -> "<blockquote type=\"cite\">$quotedBody</blockquote>"
            1 -> "<div class=\"gmail_quote\">$quotedBody</div>"
            else -> "<blockquote class=\"gmail_quote\">$quotedBody</blockquote>"
        }

        // Where the sender's text sits relative to the citation.
        val (head, citation) = when (random.next(3)) {
            0 -> "<p>$fresh</p>" to "<div class=\"moz-cite-prefix\">$attribution<br></div>"
            1 -> "" to "<div class=\"moz-cite-prefix\">$fresh<br><br>$attribution<br></div>"
            else -> "<p>$fresh</p><p>Mit freundlichen Gruessen</p>" to "<p>$attribution</p>"
        }

        val gap = if (random.next(2) == 1) "\n" else ""
        val wrapped = when (random.next(3)) {
            0 -> "$citation$gap$container"
            1 -> "<div class=\"moz-forward-container\">$citation$gap$container</div>"
            else -> "<div>$citation$gap$container</div>"
        }

        return when (random.next(5)) {
            0 -> FoldCase(
                "s$index-bottom",
                "$head$wrapped<p>${fresh}b</p>",
                listOf(fresh, "${fresh}b"),
                quoted,
                folds = false,
            )

            // Where the signature lands depends on whether the client
            // wrapped the citation: a signature outside that wrapper
            // stays outside the fold, since only the citation's own
            // siblings are absorbed. The enumerated corpus above
            // measures the unwrapped shape, where it folds.
            1 -> FoldCase(
                "s$index-signed",
                "$head$wrapped<p></p><p>-- </p><p>S$index</p>",
                listOf(fresh),
                quoted + attributionText(attribution),
                folds = true,
            )

            2 -> FoldCase(
                "s$index-separators",
                "$head$wrapped<br>   ",
                listOf(fresh),
                quoted + attributionText(attribution),
                folds = true,
            )

            else -> FoldCase(
                "s$index-plain",
                "$head$wrapped",
                listOf(fresh),
                quoted + attributionText(attribution),
                folds = true,
            )
        }
    }

    /** A run of the attribution line that survives into the output. */
    private fun attributionText(attribution: String): String = when {
        attribution.startsWith("On Mon") -> "On Mon, 15 Sep 2026 at 18:21"
        attribution.startsWith("Am ") -> "Am 15.09.26 um 18:21 schrieb"
        attribution.startsWith("Alice") -> "Alice Example schrieb am 15.09.26"
        else -> "Le 15 septembre 2026"
    }

    /** A linear congruential sequence, so a corpus is the same every run. */
    private class Lcg(private var state: Long) {
        fun next(bound: Int): Int {
            state = (state * 6364136223846793005L + 1442695040888963407L)
            val bits = (state ushr 33).toInt() and Int.MAX_VALUE
            return bits % bound
        }
    }

    @Test
    fun theCorporaAreDifferentBodies() {
        val enumerated = enumeratedCorpus().map { it.html }.toSet()
        val seeded = seededCorpus().map { it.html }.toSet()

        assertFalse(
            enumerated.any { it in seeded },
            "the two corpora share a body, so the second measures nothing new",
        )
    }

    private companion object {
        const val DETAILS = "<details class=\"herold-quoted\">"
        const val SIGNATURE = "<p>-- </p>"
    }
}
