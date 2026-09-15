package com.netzhansa.herold.shared.auth

import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

/** What the six-digit sheet shows while an elevation is being collected. */
data class StepUpPrompt(
    /** The last attempt's refusal, shown under the field. */
    val error: String? = null,
    /** A code is with the server right now. */
    val submitting: Boolean = false,
    /** The account has no authenticator app; there is no code to enter. */
    val enrollRequired: Boolean = false,
)

/**
 * The step-up sheet's half of an elevated operation (REQ-AND-AUTH-20).
 * A call the server refused with `step_up_required` suspends in
 * [elevate] while the sheet collects a six-digit code; a wrong code and
 * a code whose window has passed leave the sheet up with the server's
 * refusal on it, so the user answers again without losing the operation
 * they started.
 *
 * One sheet at a time: a second refused call waits for the one in front
 * of it. An elevation that just succeeded is reused for [COALESCE_MILLIS]
 * so two calls refused together do not ask for two codes. That window
 * only coalesces callers - the server's own elevation window is what
 * decides when a code is asked for again, and the next refusal after it
 * lapses raises the sheet once more.
 */
class StepUpCoordinator(
    private val client: StepUpClient,
    private val now: () -> Long,
) : StepUpGate {

    private val _prompt = MutableStateFlow<StepUpPrompt?>(null)

    /** Non-null while the sheet is up. */
    val prompt: StateFlow<StepUpPrompt?> = _prompt.asStateFlow()

    private val oneAtATime = Mutex()
    private var submissions: Channel<String?>? = null
    private var elevatedAt: Long? = null

    override suspend fun elevate(): Boolean = oneAtATime.withLock {
        val last = elevatedAt
        if (last != null && now() - last < COALESCE_MILLIS) return@withLock true
        val codes = Channel<String?>(Channel.BUFFERED)
        submissions = codes
        _prompt.value = StepUpPrompt()
        try {
            while (true) {
                val code = codes.receive() ?: return@withLock false
                _prompt.value = StepUpPrompt(submitting = true)
                when (val outcome = client.elevate(code)) {
                    is StepUpOutcome.Elevated -> {
                        elevatedAt = now()
                        return@withLock true
                    }

                    is StepUpOutcome.Refused -> _prompt.value = StepUpPrompt(error = outcome.message)

                    is StepUpOutcome.RateLimited -> _prompt.value = StepUpPrompt(error = outcome.message)

                    is StepUpOutcome.EnrollRequired -> _prompt.value =
                        StepUpPrompt(error = outcome.message, enrollRequired = true)

                    is StepUpOutcome.Transport -> _prompt.value = StepUpPrompt(error = outcome.message)
                }
            }
            @Suppress("UNREACHABLE_CODE")
            false
        } finally {
            submissions = null
            _prompt.value = null
        }
    }

    /** The sheet's confirm button. */
    fun submit(code: String) {
        submissions?.trySend(code)
    }

    /** The sheet was dismissed; the operation behind it is abandoned. */
    fun cancel() {
        submissions?.trySend(null)
    }

    private companion object {
        /** How long one elevation answers for callers refused together. */
        const val COALESCE_MILLIS = 5_000L
    }
}
