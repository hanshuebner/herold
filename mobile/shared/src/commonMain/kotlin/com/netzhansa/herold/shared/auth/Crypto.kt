package com.netzhansa.herold.shared.auth

/**
 * The two primitives PKCE needs, in common code so the iOS half of the
 * shared module inherits them: SHA-256 (RFC 6234) and unpadded base64url
 * (RFC 4648 section 5). The PKCE `S256` challenge is
 * `base64url(sha256(ascii(verifier)))` (RFC 7636 section 4.2), which the
 * server recomputes and compares (internal/directory/oauth2.go).
 */
internal object Sha256 {

    fun digest(input: ByteArray): ByteArray {
        val h = INITIAL.copyOf()
        val padded = pad(input)
        val w = IntArray(64)
        var offset = 0
        while (offset < padded.size) {
            for (i in 0 until 16) {
                val base = offset + i * 4
                w[i] = ((padded[base].toInt() and 0xFF) shl 24) or
                    ((padded[base + 1].toInt() and 0xFF) shl 16) or
                    ((padded[base + 2].toInt() and 0xFF) shl 8) or
                    (padded[base + 3].toInt() and 0xFF)
            }
            for (i in 16 until 64) {
                val s0 = rotr(w[i - 15], 7) xor rotr(w[i - 15], 18) xor (w[i - 15] ushr 3)
                val s1 = rotr(w[i - 2], 17) xor rotr(w[i - 2], 19) xor (w[i - 2] ushr 10)
                w[i] = w[i - 16] + s0 + w[i - 7] + s1
            }
            var a = h[0]
            var b = h[1]
            var c = h[2]
            var d = h[3]
            var e = h[4]
            var f = h[5]
            var g = h[6]
            var hh = h[7]
            for (i in 0 until 64) {
                val s1 = rotr(e, 6) xor rotr(e, 11) xor rotr(e, 25)
                val ch = (e and f) xor (e.inv() and g)
                val temp1 = hh + s1 + ch + K[i] + w[i]
                val s0 = rotr(a, 2) xor rotr(a, 13) xor rotr(a, 22)
                val maj = (a and b) xor (a and c) xor (b and c)
                val temp2 = s0 + maj
                hh = g
                g = f
                f = e
                e = d + temp1
                d = c
                c = b
                b = a
                a = temp1 + temp2
            }
            h[0] += a
            h[1] += b
            h[2] += c
            h[3] += d
            h[4] += e
            h[5] += f
            h[6] += g
            h[7] += hh
            offset += 64
        }
        val out = ByteArray(32)
        for (i in 0 until 8) {
            out[i * 4] = (h[i] ushr 24).toByte()
            out[i * 4 + 1] = (h[i] ushr 16).toByte()
            out[i * 4 + 2] = (h[i] ushr 8).toByte()
            out[i * 4 + 3] = h[i].toByte()
        }
        return out
    }

    /** The 0x80 terminator, zero fill, and the 64-bit big-endian bit length. */
    private fun pad(input: ByteArray): ByteArray {
        val bitLength = input.size.toLong() * 8
        var paddedSize = input.size + 1
        while (paddedSize % 64 != 56) paddedSize++
        val out = ByteArray(paddedSize + 8)
        input.copyInto(out)
        out[input.size] = 0x80.toByte()
        for (i in 0 until 8) {
            out[out.size - 1 - i] = (bitLength ushr (8 * i)).toByte()
        }
        return out
    }

    private fun rotr(value: Int, bits: Int): Int = (value ushr bits) or (value shl (32 - bits))

    private val INITIAL = intArrayOf(
        0x6a09e667, -0x4498517b, 0x3c6ef372, -0x5ab00ac6,
        0x510e527f, -0x64fa9774, 0x1f83d9ab, 0x5be0cd19,
    )

    private val K = intArrayOf(
        0x428a2f98, 0x71374491, -0x4a3f0431, -0x164a245b,
        0x3956c25b, 0x59f111f1, -0x6dc07d5c, -0x54e3a12b,
        -0x27f85568, 0x12835b01, 0x243185be, 0x550c7dc3,
        0x72be5d74, -0x7f214e02, -0x6423f959, -0x3e640e8c,
        -0x1b64963f, -0x1041b87a, 0x0fc19dc6, 0x240ca1cc,
        0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
        -0x67c1aeae, -0x57ce3993, -0x4ffcd838, -0x40a68039,
        -0x391ff40d, -0x2a586eb9, 0x06ca6351, 0x14292967,
        0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
        0x650a7354, 0x766a0abb, -0x7e3d36d2, -0x6d8dd37b,
        -0x5d40175f, -0x57e599b5, -0x3db47490, -0x3893ae5d,
        -0x2e6d17e7, -0x2966f9dc, -0xbf1ca7b, 0x106aa070,
        0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5,
        0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
        0x748f82ee, 0x78a5636f, -0x7b3787ec, -0x7338fdf8,
        -0x6f410006, -0x5baf9315, -0x41065c09, -0x398e870e,
    )
}

/**
 * Unpadded base64url (RFC 4648 section 5): the encoding PKCE and the
 * OAuth2 `state` use, and the one the push subscription's RFC 8291 keys
 * go on the wire in.
 */
internal object Base64Url {
    private const val ALPHABET = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

    fun encode(bytes: ByteArray): String {
        val out = StringBuilder((bytes.size * 4 + 2) / 3)
        var i = 0
        while (i + 2 < bytes.size) {
            val chunk = ((bytes[i].toInt() and 0xFF) shl 16) or
                ((bytes[i + 1].toInt() and 0xFF) shl 8) or
                (bytes[i + 2].toInt() and 0xFF)
            out.append(ALPHABET[(chunk ushr 18) and 0x3F])
            out.append(ALPHABET[(chunk ushr 12) and 0x3F])
            out.append(ALPHABET[(chunk ushr 6) and 0x3F])
            out.append(ALPHABET[chunk and 0x3F])
            i += 3
        }
        when (bytes.size - i) {
            1 -> {
                val chunk = (bytes[i].toInt() and 0xFF) shl 16
                out.append(ALPHABET[(chunk ushr 18) and 0x3F])
                out.append(ALPHABET[(chunk ushr 12) and 0x3F])
            }
            2 -> {
                val chunk = ((bytes[i].toInt() and 0xFF) shl 16) or ((bytes[i + 1].toInt() and 0xFF) shl 8)
                out.append(ALPHABET[(chunk ushr 18) and 0x3F])
                out.append(ALPHABET[(chunk ushr 12) and 0x3F])
                out.append(ALPHABET[(chunk ushr 6) and 0x3F])
            }
        }
        return out.toString()
    }

    /**
     * Decodes unpadded (or padded) base64url. Throws on a character
     * outside the alphabet, so a malformed stored key is a failure rather
     * than silently wrong bytes.
     */
    fun decode(text: String): ByteArray {
        val trimmed = text.trimEnd('=')
        val out = ByteArray(trimmed.length * 3 / 4)
        var buffer = 0
        var bits = 0
        var written = 0
        for (ch in trimmed) {
            val value = ALPHABET.indexOf(ch)
            require(value >= 0) { "not base64url: $ch" }
            buffer = (buffer shl 6) or value
            bits += 6
            if (bits >= 8) {
                bits -= 8
                out[written++] = ((buffer ushr bits) and 0xFF).toByte()
            }
        }
        return if (written == out.size) out else out.copyOfRange(0, written)
    }
}
