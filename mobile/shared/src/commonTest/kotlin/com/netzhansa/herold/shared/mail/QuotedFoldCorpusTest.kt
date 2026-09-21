package com.netzhansa.herold.shared.mail

import com.netzhansa.herold.shared.mail.QuotedFoldCorpus.FoldCase
import kotlin.test.Test
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * The fold measured against every body in [QuotedFoldCorpus], on one
 * invariant: what the sender wrote renders outside the fold, and what
 * the quoted message carries renders inside it (issue #432).
 *
 * The named fixtures in `QuotedHistoryTest` are the shapes a client is
 * known to emit. These are combinations of them - a wrapper around a
 * bundle around a nested quote, each one again with a paragraph at
 * either end - because every defect this pass has produced came from a
 * shape reached through another shape, or from a body that simply did
 * not begin at its quote.
 */
class QuotedFoldCorpusTest {

    @Test
    fun theEnumeratedCorpusFoldsTheQuoteAndOnlyTheQuote() =
        check(QuotedFoldCorpus.enumeratedCorpus())

    @Test
    fun theSeededCorpusFoldsTheQuoteAndOnlyTheQuote() =
        check(QuotedFoldCorpus.seededCorpus())

    @Test
    fun theEnumeratedCorpusHoldsWithAParagraphAroundIt() =
        check(QuotedFoldCorpus.withTextAround(QuotedFoldCorpus.enumeratedCorpus()))

    @Test
    fun theSeededCorpusHoldsWithAParagraphAroundIt() =
        check(QuotedFoldCorpus.withTextAround(QuotedFoldCorpus.seededCorpus()))

    @Test
    fun theCorporaAreDifferentBodies() {
        val enumerated = QuotedFoldCorpus.enumeratedCorpus().map { it.html }.toSet()
        val seeded = QuotedFoldCorpus.seededCorpus().map { it.html }.toSet()

        assertFalse(
            enumerated.any { it in seeded },
            "the two corpora share a body, so the second measures nothing new",
        )
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

    private companion object {
        const val DETAILS = "<details class=\"herold-quoted\">"
    }
}
