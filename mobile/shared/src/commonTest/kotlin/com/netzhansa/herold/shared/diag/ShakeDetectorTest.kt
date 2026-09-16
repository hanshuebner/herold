package com.netzhansa.herold.shared.diag

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/**
 * The thresholds that decide what a shake is (REQ-AND-SYS-51). The
 * detector is pure arithmetic, so the cases a device would produce -
 * lying still, being picked up, being shaken, being shaken twice - are
 * fed as samples.
 */
class ShakeDetectorTest {

    /** One gravity, as the platform reports a phone lying on a table. */
    private val restZ = 9.81

    /** What a shaken phone reads on the axis it is swung along. */
    private val shakeX = 20.0

    @Test
    fun aPhoneLyingStillNeverShakes() {
        val detector = ShakeDetector()
        var fired = 0
        repeat(100) { i ->
            if (detector.onSample(0.0, 0.0, restZ, i * 20L)) fired++
        }
        assertEquals(0, fired, "one gravity must not read as a shake")
    }

    @Test
    fun aSingleJoltIsNotAShake() {
        val detector = ShakeDetector()
        var fired = 0
        repeat(20) { i ->
            val x = if (i == 10) shakeX else 0.0
            if (detector.onSample(x, 0.0, restZ, i * 20L)) fired++
        }
        assertEquals(0, fired, "picking the phone up must not read as a shake")
    }

    @Test
    fun sustainedShakingFiresOnce() {
        val detector = ShakeDetector()
        val fired = feedShake(detector, samples = 12, startMs = 0)
        assertEquals(1, fired, "a shake is reported once, not once per sample")
    }

    @Test
    fun theCooldownSwallowsTheShakingThatFollows() {
        val detector = ShakeDetector()
        assertEquals(1, feedShake(detector, samples = 12, startMs = 0))
        // Still inside the cooldown: the sheet is coming up and the
        // phone is still moving.
        assertEquals(0, feedShake(detector, samples = 12, startMs = 500))
        assertEquals(0, feedShake(detector, samples = 12, startMs = 1_500))
    }

    @Test
    fun aShakeAfterTheCooldownFiresAgain() {
        val detector = ShakeDetector()
        assertEquals(1, feedShake(detector, samples = 12, startMs = 0))
        assertEquals(1, feedShake(detector, samples = 12, startMs = 5_000))
    }

    @Test
    fun anOldWindowDoesNotAccumulateIntoAShake() {
        val detector = ShakeDetector()
        var fired = 0
        // One accelerating sample a second: never four inside 500 ms.
        repeat(10) { i ->
            if (detector.onSample(shakeX, 0.0, 0.0, i * 1_000L)) fired++
        }
        assertEquals(0, fired, "samples a second apart are not one shake")
    }

    @Test
    fun aMostlyStillWindowIsNotAShake() {
        val detector = ShakeDetector()
        var fired = 0
        // Half the window accelerating: below the three-quarters rule.
        repeat(40) { i ->
            val x = if (i % 2 == 0) shakeX else 0.0
            if (detector.onSample(x, 0.0, restZ, i * 20L)) fired++
        }
        assertEquals(0, fired, "half a window of movement is not a shake")
    }

    @Test
    fun resetForgetsTheCooldown() {
        val detector = ShakeDetector()
        assertTrue(feedShake(detector, samples = 12, startMs = 0) == 1)
        detector.reset()
        assertEquals(1, feedShake(detector, samples = 12, startMs = 500))
    }

    @Test
    fun theThresholdSitsAboveGravityAndBelowAShake() {
        assertTrue(ShakeDetector.DEFAULT_ACCELERATION_THRESHOLD > 9.81)
        assertFalse(ShakeDetector.DEFAULT_ACCELERATION_THRESHOLD > shakeX)
    }

    /** Feeds one burst of shaking at 20 ms intervals; returns how often it fired. */
    private fun feedShake(detector: ShakeDetector, samples: Int, startMs: Long): Int {
        var fired = 0
        repeat(samples) { i ->
            // Alternating direction, as swinging the phone produces.
            val x = if (i % 2 == 0) shakeX else -shakeX
            if (detector.onSample(x, 0.0, restZ, startMs + i * 20L)) fired++
        }
        return fired
    }
}
