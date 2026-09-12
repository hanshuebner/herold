package com.netzhansa.herold.shared.push

/**
 * A P-256 key pair in the wire forms RFC 8291 uses: [publicKey] is the
 * 65-byte uncompressed SEC1 point (0x04 || X || Y) the subscription
 * registers as `p256dh`, [privateKey] is the 32-byte big-endian scalar.
 */
class P256KeyPair(val publicKey: ByteArray, val privateKey: ByteArray)

/**
 * The primitives the push envelope needs from the platform: P-256 key
 * agreement, HMAC-SHA-256 and AES-128-GCM. The derivation around them
 * (RFC 8188 record framing, RFC 8291 key derivation) is common code, so
 * the iOS half of the shared module inherits it by supplying these four
 * actuals.
 */
expect fun generateP256KeyPair(): P256KeyPair

/** The raw ECDH shared secret (the X coordinate) for [privateKey] and [peerPublicKey]. */
expect fun p256SharedSecret(privateKey: ByteArray, peerPublicKey: ByteArray): ByteArray

expect fun hmacSha256(key: ByteArray, data: ByteArray): ByteArray

/** Decrypts [ciphertext] (the GCM tag appended) and throws when the tag does not verify. */
expect fun aesGcmOpen(key: ByteArray, nonce: ByteArray, ciphertext: ByteArray): ByteArray
