package com.netzhansa.herold.android.ui.inbox

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Badge
import androidx.compose.material3.ScrollableTabRow
import androidx.compose.material3.Tab
import androidx.compose.material3.TabRowDefaults
import androidx.compose.material3.TabRowDefaults.tabIndicatorOffset
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.dp
import com.netzhansa.herold.shared.domain.Keywords

/** The badge's slot, wide enough for the longest count the tab shows. */
private val BADGE_SLOT = 30.dp

/** The gap between a tab's name and its badge slot. */
private val BADGE_GAP = 4.dp

/** The highest count a badge spells out; past it the badge reads "99+". */
private const val COUNT_CAP = 99

/**
 * The inbox's lanes as a tab row (REQ-AND-CAT-01, suite REQ-CAT-04):
 * one tab per pinned category in the account's priority order, the
 * primary-role lane leading. Each tab carries the unread count of the
 * mail it holds, and the badge sits in a slot the tab keeps in every
 * state, so the row's geometry is the same whether a lane is unread,
 * read, or counts into the hundreds: the list below never moves under
 * the reader when a message arrives or is opened (issue #427, the rule
 * issue #421 set for the status dot).
 */
@Composable
fun CategoryTabRow(
    tabs: List<String>,
    selected: String?,
    unreadByLane: Map<String, Int>,
    onSelect: (String) -> Unit,
    modifier: Modifier = Modifier,
) {
    val selectedTabIndex = tabs.indexOf(selected).coerceAtLeast(0)
    ScrollableTabRow(
        selectedTabIndex = selectedTabIndex,
        edgePadding = 8.dp,
        // The indicator draws over the tab the row hands it and skips the
        // frame in which there is none (issue #447). The selected index and
        // the measured tab positions are read from two states, so a lane
        // that goes - the last message of a category archived - can leave
        // an index from the row of two against the positions of the row of
        // one for a frame, which is what the stock indicator indexes past
        // the end of.
        indicator = { positions ->
            indicatorTab(positions.size, selectedTabIndex)?.let { index ->
                TabRowDefaults.SecondaryIndicator(Modifier.tabIndicatorOffset(positions[index]))
            }
        },
        modifier = modifier.testTag("inbox-tabs"),
    ) {
        tabs.forEach { category ->
            val unread = unreadByLane[category] ?: 0
            Tab(
                selected = selected == category,
                onClick = { onSelect(category) },
                text = { TabLabel(category = category, unread = unread) },
                modifier = Modifier.testTag("inbox-tab-$category"),
            )
        }
    }
}

/** A tab's name beside its badge slot. */
@Composable
private fun TabLabel(category: String, unread: Int) {
    val name = Keywords.categoryLabel(category)
    Row(
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(BADGE_GAP),
    ) {
        Text(text = name)
        Box(
            modifier = Modifier.width(BADGE_SLOT),
            contentAlignment = Alignment.Center,
        ) {
            if (unread > 0) {
                val description = "$name, $unread unread"
                Badge(
                    modifier = Modifier
                        .semantics { contentDescription = description }
                        .testTag("inbox-tab-badge-$category"),
                ) {
                    Text(text = badgeText(unread))
                }
            }
        }
    }
}

/**
 * The tab the indicator draws over, given how many tabs were measured
 * ([tabCount]) and which one the row is on ([selectedTabIndex]); null
 * when the selection names a tab the measurement does not hold.
 *
 * The two values reach the indicator from different states and can
 * disagree for a frame while the lane set shrinks, so the selection is
 * bounded against the positions in hand rather than against the list
 * the caller composed.
 */
internal fun indicatorTab(tabCount: Int, selectedTabIndex: Int): Int? =
    selectedTabIndex.takeIf { it in 0 until tabCount }

/** What the badge reads: the count, or the cap once it is past it. */
fun badgeText(unread: Int): String = if (unread > COUNT_CAP) "$COUNT_CAP+" else unread.toString()
