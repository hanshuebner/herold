package com.netzhansa.herold.android.ui.common

import androidx.compose.foundation.interaction.DragInteraction
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.Saver
import androidx.compose.runtime.saveable.listSaver
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.snapshots.Snapshot

/**
 * Where each message list was left, held per destination (issue #439,
 * REQ-AND-NAV-25).
 *
 * Navigation Compose disposes a destination's composition while the
 * destination stays on the back stack, so a list state held in a plain
 * `remember` is rebuilt at the top by the time back returns. This holder
 * lives in the screen's saved state, so it outlives both the trip into a
 * conversation and the process: a restored shell puts each list back
 * where it was, as it already does with the selected lane and the open
 * destination (REQ-AND-NAV-20).
 *
 * One place per key, and a key per lane, mailbox and label, so switching
 * between two destinations gives each its own offset instead of carrying
 * one over. A reader's whole session would name as many keys as they
 * have ever opened labels, so only the [LIMIT] most recently used are
 * kept; a destination that falls out of that window opens at its newest
 * message, which is where it opens on a fresh start anyway.
 */
class ListPositions internal constructor(restored: List<Position>) {

    /** Where one list stands: its leading item, and whether the reader put it there. */
    data class Position(
        val key: String,
        val index: Int,
        val offset: Int,
        /** True once the reader has dragged this list, so it is not pinned to its newest. */
        val scrolled: Boolean,
    )

    /** The places, least recently asked for first. */
    private val held = LinkedHashMap<String, Position>().apply {
        restored.forEach { put(it.key, it) }
    }

    /** The states in the composition, which carry the live offsets. */
    private val live = HashMap<String, LazyListState>()

    /** The places a list has been given but has not been able to take yet. */
    private val pending = HashMap<String, Position>()

    /** The state of the list [key] names, starting where it was left. */
    fun state(key: String): LazyListState {
        val position = touch(key)
        return live.getOrPut(key) {
            if (position.index > 0 || position.offset > 0) pending[key] = position
            LazyListState(position.index, position.offset)
        }
    }

    /**
     * Holds the place the list [key] names stands at, for a list that is
     * about to be measured with no rows: the destination's rows are
     * re-collected from the store when it is opened, and a LazyColumn
     * measured empty reads its own offset back as zero.
     *
     * The read is unobserved, so a screen asking this on every
     * composition is not subscribed to the list's scrolling.
     */
    fun holdPlace(key: String) {
        if (pending.containsKey(key)) return
        val state = live[key] ?: return
        val index = Snapshot.withoutReadObservation { state.firstVisibleItemIndex }
        val offset = Snapshot.withoutReadObservation { state.firstVisibleItemScrollOffset }
        if (index > 0 || offset > 0) pending[key] = Position(key, index, offset, scrolled(key))
    }

    /**
     * Puts the list [key] names back where it was left, once it has rows
     * to hold that place. Until it has been taken, the held place is
     * what this holder reports and saves.
     */
    suspend fun applyPending(key: String, state: LazyListState) {
        val position = pending.remove(key) ?: return
        if (state.firstVisibleItemIndex != position.index ||
            state.firstVisibleItemScrollOffset != position.offset
        ) {
            state.scrollToItem(position.index, position.offset)
        }
    }

    /** True once the reader has dragged the list [key] names. */
    fun scrolled(key: String): Boolean = held[key]?.scrolled == true

    /** Records that the reader has dragged the list [key] names. */
    fun markScrolled(key: String) {
        held[key] = current(key).copy(scrolled = true)
    }

    /** The places worth keeping, least recently used first. */
    internal fun positions(): List<Position> = held.keys.toList().map { current(it) }

    /** [key]'s place, read from its live state when it has one. */
    private fun current(key: String): Position {
        val was = held[key] ?: Position(key, 0, 0, false)
        pending[key]?.let { return it.copy(scrolled = was.scrolled) }
        val state = live[key] ?: return was
        return was.copy(
            index = state.firstVisibleItemIndex,
            offset = state.firstVisibleItemScrollOffset,
        )
    }

    /** Moves [key] to the most recent end and drops what no longer fits. */
    private fun touch(key: String): Position {
        val position = held.remove(key) ?: Position(key, 0, 0, false)
        held[key] = position
        while (held.size > LIMIT) {
            val oldest = held.keys.first()
            held.remove(oldest)
            live.remove(oldest)
            pending.remove(oldest)
        }
        return position
    }

    companion object {
        /**
         * How many lists' places are kept. A reader moves between a
         * handful of destinations; a dozen covers the inbox's lanes, the
         * system folders and the labels in use at once.
         */
        const val LIMIT = 12

        /** The saved form: four strings per place, in recency order. */
        val Saver: Saver<ListPositions, Any> = listSaver<ListPositions, String>(
            save = { positions: ListPositions ->
                positions.positions().flatMap { position ->
                    listOf(
                        position.key,
                        position.index.toString(),
                        position.offset.toString(),
                        if (position.scrolled) "1" else "0",
                    )
                }
            },
            restore = { saved: List<String> ->
                ListPositions(
                    saved.chunked(FIELDS).filter { it.size == FIELDS }.map { fields ->
                        Position(
                            key = fields[0],
                            index = fields[1].toIntOrNull() ?: 0,
                            offset = fields[2].toIntOrNull() ?: 0,
                            scrolled = fields[3] == "1",
                        )
                    },
                )
            },
        )

        /** How many strings one place takes in the saved form. */
        private const val FIELDS = 4
    }
}

/** The screen's list places, restored with the rest of its saved state. */
@Composable
fun rememberListPositions(): ListPositions =
    rememberSaveable(saver = ListPositions.Saver) { ListPositions(emptyList()) }

/**
 * The state of the list [key] names, starting where the reader left it.
 * [hasRows] says whether the list has anything to scroll, so a place is
 * applied once its rows are there.
 *
 * The drag the reader makes is what marks the list as theirs to place:
 * a programmatic scroll - the pin that keeps an untouched list at its
 * newest message - raises no drag interaction, so it does not read as
 * the reader having scrolled.
 */
@Composable
fun ListPositions.listState(key: String, hasRows: Boolean = true): LazyListState {
    val state = remember(this, key) { state(key) }
    if (!hasRows) holdPlace(key)
    LaunchedEffect(state, key) {
        state.interactionSource.interactions.collect { interaction ->
            if (interaction is DragInteraction.Start) markScrolled(key)
        }
    }
    LaunchedEffect(state, key, hasRows) {
        if (hasRows) applyPending(key, state)
    }
    return state
}
