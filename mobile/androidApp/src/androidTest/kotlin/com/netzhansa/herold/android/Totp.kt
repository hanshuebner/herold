package com.netzhansa.herold.android

import java.nio.ByteBuffer
import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec

/**
 * RFC 6238 TOTP over the dev instance's printed admin secret, so the
 * acceptance run can exercise the sign-in form's two-factor field with a
 * code the server accepts (scripts/dev-instance.sh prints
 * ADMIN_TOTP_SECRET).
 */
object Totp {
    fun code(base32Secret: String, timeSeconds: Long = System.currentTimeMillis() / 1000): String {
        val key = base32Decode(base32Secret)
        val mac = Mac.getInstance("HmacSHA1")
        mac.init(SecretKeySpec(key, "HmacSHA1"))
        val digest = mac.doFinal(ByteBuffer.allocate(8).putLong(timeSeconds / 30).array())
        val offset = digest[digest.size - 1].toInt() and 0x0F
        val binary = ((digest[offset].toInt() and 0x7F) shl 24) or
            ((digest[offset + 1].toInt() and 0xFF) shl 16) or
            ((digest[offset + 2].toInt() and 0xFF) shl 8) or
            (digest[offset + 3].toInt() and 0xFF)
        return (binary % 1_000_000).toString().padStart(6, '0')
    }

    private const val ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

    private fun base32Decode(input: String): ByteArray {
        val clean = input.trim().trimEnd('=').uppercase()
        var buffer = 0
        var bits = 0
        val out = ArrayList<Byte>(clean.length * 5 / 8)
        clean.forEach { char ->
            val value = ALPHABET.indexOf(char)
            require(value >= 0) { "not base32: $char" }
            buffer = (buffer shl 5) or value
            bits += 5
            if (bits >= 8) {
                bits -= 8
                out.add(((buffer shr bits) and 0xFF).toByte())
            }
        }
        return out.toByteArray()
    }
}
