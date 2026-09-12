package com.netzhansa.herold.shared.push

import com.netzhansa.herold.shared.auth.Base64Url
import com.netzhansa.herold.shared.auth.secureRandomBytes

/**
 * The RFC 8291 key material one subscription holds: the P-256 key pair
 * whose public half herold encrypts to, and the 16-byte auth secret that
 * salts the key derivation. Generated on the device and kept in
 * Keystore-backed storage - the private key and the auth secret are
 * credentials, so they never reach the local database or a log.
 */
class WebPushKeys(
    val publicKey: ByteArray,
    val privateKey: ByteArray,
    val authSecret: ByteArray,
) {
    /** The `keys.p256dh` wire value: base64url of the uncompressed point. */
    val p256dh: String get() = Base64Url.encode(publicKey)

    /** The `keys.auth` wire value. */
    val auth: String get() = Base64Url.encode(authSecret)

    /** The private scalar in the form [fromStored] reads back. */
    val storedPrivateKey: String get() = Base64Url.encode(privateKey)

    companion object {
        /** RFC 8291 section 3.2: the auth secret is 16 bytes. */
        const val AUTH_SECRET_LEN = 16

        /** Mints a fresh key pair and auth secret for a new subscription. */
        fun generate(): WebPushKeys {
            val pair = generateP256KeyPair()
            return WebPushKeys(
                publicKey = pair.publicKey,
                privateKey = pair.privateKey,
                authSecret = secureRandomBytes(AUTH_SECRET_LEN),
            )
        }

        /** Restores keys from their stored base64url forms, null when any part is missing. */
        fun fromStored(p256dh: String?, privateKey: String?, auth: String?): WebPushKeys? {
            val public = p256dh?.let { Base64Url.decode(it) } ?: return null
            val private = privateKey?.let { Base64Url.decode(it) } ?: return null
            val secret = auth?.let { Base64Url.decode(it) } ?: return null
            return WebPushKeys(public, private, secret)
        }
    }
}

/** The body was not a well-formed aes128gcm envelope, or its tag did not verify. */
class PushDecryptionException(message: String, cause: Throwable? = null) : Exception(message, cause)

/**
 * Decrypts the `aes128gcm` push envelope herold POSTs to a Web Push or
 * UnifiedPush endpoint (RFC 8188 content coding, RFC 8291 key
 * derivation). The envelope is
 *
 *     salt(16) || rs(4, big endian) || idlen(1) || keyid(idlen) || ciphertext
 *
 * where keyid is the sender's ephemeral P-256 public key and the
 * plaintext ends with the RFC 8188 section 2 padding delimiter (0x02 on
 * the last record) followed by zero padding.
 *
 * herold sends one record per push (`internal/webpush/encrypt.go`), so a
 * body announcing a record size smaller than its ciphertext carries a
 * continuation this decoder does not reassemble and is rejected.
 */
object WebPushEnvelope {

    private const val SALT_LEN = 16
    private const val HEADER_LEN = SALT_LEN + 4 + 1
    private const val P256_UNCOMPRESSED_LEN = 65
    private const val GCM_TAG_LEN = 16
    private const val CEK_LEN = 16
    private const val NONCE_LEN = 12

    /** The RFC 8188 and 8291 info strings are NUL-terminated. */
    private const val NUL = "\u0000"
    private val KEY_INFO = "Content-Encoding: aes128gcm$NUL".encodeToByteArray()
    private val NONCE_INFO = "Content-Encoding: nonce$NUL".encodeToByteArray()
    private val AUTH_INFO_PREFIX = "WebPush: info$NUL".encodeToByteArray()

    /** The decrypted payload, which for herold is the JMAP envelope as UTF-8 JSON. */
    fun decrypt(body: ByteArray, keys: WebPushKeys): ByteArray {
        if (body.size < HEADER_LEN) throw PushDecryptionException("body shorter than the aes128gcm header")
        val salt = body.copyOfRange(0, SALT_LEN)
        val recordSize = ((body[16].toInt() and 0xFF) shl 24) or
            ((body[17].toInt() and 0xFF) shl 16) or
            ((body[18].toInt() and 0xFF) shl 8) or
            (body[19].toInt() and 0xFF)
        val keyIdLen = body[20].toInt() and 0xFF
        if (keyIdLen != P256_UNCOMPRESSED_LEN) {
            throw PushDecryptionException("keyid is $keyIdLen bytes, want an uncompressed P-256 point")
        }
        if (body.size < HEADER_LEN + keyIdLen + GCM_TAG_LEN + 1) {
            throw PushDecryptionException("body carries no complete record")
        }
        val senderPublic = body.copyOfRange(HEADER_LEN, HEADER_LEN + keyIdLen)
        val ciphertext = body.copyOfRange(HEADER_LEN + keyIdLen, body.size)
        if (recordSize in 1 until ciphertext.size) {
            throw PushDecryptionException("body carries more than one record")
        }

        val shared = runCatching { p256SharedSecret(keys.privateKey, senderPublic) }
            .getOrElse { throw PushDecryptionException("ECDH with the sender key failed", it) }

        // RFC 8291 section 3.3: the IKM binds the shared secret to both
        // public keys and the recipient's auth secret, so a body encrypted
        // for another subscription cannot be replayed into this one.
        val info = AUTH_INFO_PREFIX + keys.publicKey + senderPublic
        val ikm = hkdfExpand(hkdfExtract(keys.authSecret, shared), info, 32)
        val prk = hkdfExtract(salt, ikm)
        val cek = hkdfExpand(prk, KEY_INFO, CEK_LEN)
        val nonce = hkdfExpand(prk, NONCE_INFO, NONCE_LEN)

        val padded = runCatching { aesGcmOpen(cek, nonce, ciphertext) }
            .getOrElse { throw PushDecryptionException("the record's authentication tag did not verify", it) }
        return unpad(padded)
    }

    /** Decrypts and decodes the payload as UTF-8 text. */
    fun decryptToText(body: ByteArray, keys: WebPushKeys): String = decrypt(body, keys).decodeToString()

    /**
     * RFC 8188 section 2: the plaintext ends with a delimiter - 0x02 on
     * the last record, 0x01 otherwise - followed by zero padding.
     */
    private fun unpad(padded: ByteArray): ByteArray {
        var end = padded.size - 1
        while (end >= 0 && padded[end].toInt() == 0) end--
        if (end < 0) throw PushDecryptionException("record carries no padding delimiter")
        val delimiter = padded[end].toInt()
        if (delimiter != 1 && delimiter != 2) {
            throw PushDecryptionException("record padding delimiter is 0x${delimiter.toString(16)}")
        }
        return padded.copyOfRange(0, end)
    }

    /** HKDF-Extract (RFC 5869 section 2.2). */
    private fun hkdfExtract(salt: ByteArray, ikm: ByteArray): ByteArray = hmacSha256(salt, ikm)

    /** HKDF-Expand (RFC 5869 section 2.3), single block, which covers every length here. */
    private fun hkdfExpand(prk: ByteArray, info: ByteArray, length: Int): ByteArray {
        require(length <= 32) { "single-block HKDF-Expand yields at most 32 bytes" }
        return hmacSha256(prk, info + byteArrayOf(1)).copyOfRange(0, length)
    }
}
