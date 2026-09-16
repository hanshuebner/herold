package com.netzhansa.herold.shared.diag

/**
 * Turns accelerometer samples into a shake (REQ-AND-SYS-51). The rule is
 * the one mail apps settled on: a sample counts as accelerating when the
 * magnitude of the whole vector - gravity included - is past
 * [accelerationThreshold], and a shake is declared when at least
 * [minSamples] samples inside the last [windowMs] are accelerating and
 * they are at least [acceleratingFraction] of that window's samples.
 *
 * At rest the magnitude is one gravity, about 9.81 m/s^2, so the phone
 * lying on a table never reaches the threshold; a phone carried, put
 * down or picked up reaches it for a sample or two, which the fraction
 * and the sample floor reject.
 *
 * [cooldownMs] is what makes one shake one report: the shaking that
 * continues while the sheet comes up does not open a second one.
 *
 * Pure arithmetic, so the thresholds are testable on the host JVM
 * without a device or a sensor.
 */
class ShakeDetector(
    private val accelerationThreshold: Double = DEFAULT_ACCELERATION_THRESHOLD,
    private val windowMs: Long = DEFAULT_WINDOW_MS,
    private val minSamples: Int = DEFAULT_MIN_SAMPLES,
    private val acceleratingFraction: Double = DEFAULT_ACCELERATING_FRACTION,
    private val cooldownMs: Long = DEFAULT_COOLDOWN_MS,
) {
    private class Sample(val atMs: Long, val accelerating: Boolean)

    private val window = ArrayDeque<Sample>()
    private var lastShakeAtMs: Long? = null

    /**
     * Folds one accelerometer reading in and reports whether it
     * completes a shake. The magnitudes are in m/s^2, as the platform
     * delivers them.
     */
    fun onSample(x: Double, y: Double, z: Double, atMs: Long): Boolean {
        val magnitudeSquared = x * x + y * y + z * z
        window.addLast(Sample(atMs, magnitudeSquared > accelerationThreshold * accelerationThreshold))
        while (window.size > 1 && atMs - window.first().atMs > windowMs) {
            window.removeFirst()
        }
        if (window.size < minSamples) return false
        val accelerating = window.count { it.accelerating }
        if (accelerating < minSamples) return false
        if (accelerating < window.size * acceleratingFraction) return false

        val last = lastShakeAtMs
        // The window is spent either way: the samples that made this
        // shake must not also make the next one.
        window.clear()
        if (last != null && atMs - last < cooldownMs) return false
        lastShakeAtMs = atMs
        return true
    }

    /** Forgets the window and the cooldown; the detector starts fresh. */
    fun reset() {
        window.clear()
        lastShakeAtMs = null
    }

    companion object {
        /**
         * The magnitude a sample must pass to count, in m/s^2. One
         * gravity is 9.81, so this is a third again as much as holding
         * the phone still.
         */
        const val DEFAULT_ACCELERATION_THRESHOLD = 13.0

        /** How far back the samples that make a shake may reach. */
        const val DEFAULT_WINDOW_MS = 500L

        /** How many accelerating samples a shake needs at the least. */
        const val DEFAULT_MIN_SAMPLES = 4

        /** How much of the window must be accelerating. */
        const val DEFAULT_ACCELERATING_FRACTION = 0.75

        /** How long after a shake the next one is ignored. */
        const val DEFAULT_COOLDOWN_MS = 3_000L
    }
}
