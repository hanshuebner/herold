package com.netzhansa.herold.shared.mail

/**
 * The initials-on-a-colour avatar a sender falls back to when herold
 * serves no picture for the address (issue #348, suite `REQ-MAIL-44`
 * tier 4).
 *
 * The suite draws its fallback on one interactive colour; on the phone a
 * notification's large icon is the only thing distinguishing two senders
 * at a glance, so the colour is derived from the address - stable for one
 * sender, spread across the palette between senders.
 */
object SenderAvatar {

    /**
     * The palette, taken from the Material tonal ranges that stay legible
     * under white text in both light and dark shades.
     */
    val PALETTE: List<Int> = listOf(
        0xFF1565C0.toInt(), // blue
        0xFF2E7D32.toInt(), // green
        0xFF6A1B9A.toInt(), // purple
        0xFFAD1457.toInt(), // pink
        0xFFEF6C00.toInt(), // orange
        0xFF00838F.toInt(), // teal
        0xFF4527A0.toInt(), // deep purple
        0xFF37474F.toInt(), // blue grey
        0xFFC62828.toInt(), // red
        0xFF9E6B00.toInt(), // amber
    )

    /** The background colour for [address], the same one every time. */
    fun colourFor(address: String): Int {
        val key = address.lowercase().trim()
        if (key.isEmpty()) return PALETTE.first()
        var hash = 0
        key.forEach { hash = hash * 31 + it.code }
        val index = ((hash % PALETTE.size) + PALETTE.size) % PALETTE.size
        return PALETTE[index]
    }

    /**
     * The letters drawn on it: the initials of a display name, or the
     * first letter of the address when there is no name.
     */
    fun initialsFor(name: String?, address: String): String {
        val words = name.orEmpty().trim().split(' ', '.', '_', '-').filter { it.isNotBlank() }
        val fromName = when {
            words.size >= 2 -> "${words.first().first()}${words.last().first()}"
            words.size == 1 -> words.first().take(1)
            else -> ""
        }
        val initials = fromName.ifBlank { address.trim().takeWhile { it != '@' }.take(1) }
        return initials.uppercase().ifBlank { "?" }
    }
}
