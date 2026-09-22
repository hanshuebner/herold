package com.netzhansa.herold.shared.mail

import com.netzhansa.herold.shared.mail.QuotedFoldShapes.Fixture
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * The seven shapes of [QuotedFoldShapes], each in four positions,
 * measured against the sanitiser's output (issue #456).
 *
 * This is the instrumented spec's host-JVM twin: same table, same
 * verdicts, read out of the markup instead of off a screen. It runs in
 * seconds, so it is where a rule is put back to see the spec go red -
 * a check that has only ever passed gates nothing.
 *
 * What it adds over [QuotedFoldCorpusTest], which measures generated
 * bodies: these are the exact bodies the defects were reported on, and
 * they are the exact bodies the Suite delivers in its own end-to-end
 * spec, so a divergence between the two clients shows up as a failure
 * here rather than as a difference a reader notices.
 */
class QuotedFoldShapesTest {

    @Test
    fun everyShapeFoldsWhatTheQuotedMessageCarriesAndNothingElse() {
        val failures = mutableListOf<String>()
        QuotedFoldShapes.fixtures().forEach { fixture ->
            val out = HtmlSanitizer.sanitize(fixture.html, collapseQuotes = true).html
            println("SHAPE\t${fixture.id}\t${out.replace("\n", "\\n")}")
            failures += verdicts(fixture, out)
        }
        assertTrue(failures.isEmpty(), failures.joinToString("\n"))
    }

    /** Seven shapes, four positions each: the corpus is complete. */
    @Test
    fun theTableCarriesEveryShapeInEveryPosition() {
        val fixtures = QuotedFoldShapes.fixtures()
        assertEquals(28, fixtures.size, "the shape table lost a fixture")
        assertEquals(28, fixtures.map { it.id }.toSet().size, "two fixtures share an id")
        assertEquals(
            28,
            fixtures.map { it.html }.toSet().size,
            "two fixtures carry the same body, so one of them measures nothing",
        )
    }

    /**
     * A body the reader answered under keeps its quote open. The rule
     * is the fold's own and predates every rewrite of the leading-text
     * gate (mobile e95032e5), which is why the trailing and both
     * positions expect no fold at all rather than the plain body's
     * outcome.
     */
    @Test
    fun aParagraphUnderTheQuoteHoldsTheFoldBack() {
        QuotedFoldShapes.shapes.filter { it.foldsWhenUnwrapped }.forEach { shape ->
            listOf(QuotedFoldShapes.Wrap.TRAILING, QuotedFoldShapes.Wrap.BOTH).forEach { wrap ->
                val fixture = QuotedFoldShapes.fixture(shape, wrap)
                val out = HtmlSanitizer.sanitize(fixture.html, collapseQuotes = true).html
                assertTrue(
                    !out.contains(DETAILS),
                    "${fixture.id}: folded a quote the reader wrote under: $out",
                )
            }
        }
    }

    private fun verdicts(fixture: Fixture, out: String): List<String> {
        val problems = mutableListOf<String>()
        val start = out.indexOf(DETAILS)
        val end = out.indexOf("</details>")

        if (!fixture.folds) {
            if (start >= 0) problems += "${fixture.id}: folded a body that owes no fold: $out"
            fixture.markers.forEach { marker ->
                if (!out.contains(marker.text)) {
                    problems += "${fixture.id}: \"${marker.text}\" went missing"
                }
            }
            return problems
        }

        if (start < 0) {
            problems += "${fixture.id}: nothing folded: $out"
            return problems
        }

        fixture.markers.forEach { marker ->
            val at = out.indexOf(marker.text)
            when {
                at < 0 -> problems += "${fixture.id}: \"${marker.text}\" went missing"
                marker.foldsAway && at < start ->
                    problems += "${fixture.id}: the quoted \"${marker.text}\" renders as the sender's own text"
                marker.foldsAway && at > end ->
                    problems += "${fixture.id}: the quoted \"${marker.text}\" fell out of the fold"
                !marker.foldsAway && at > start ->
                    problems += "${fixture.id}: the sender's \"${marker.text}\" folded away"
            }
        }
        return problems
    }

    private companion object {
        const val DETAILS = "<details class=\"herold-quoted\">"
    }
}
