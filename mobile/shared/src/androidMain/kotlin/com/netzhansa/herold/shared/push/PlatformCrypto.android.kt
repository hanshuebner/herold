package com.netzhansa.herold.shared.push

import java.math.BigInteger
import java.security.AlgorithmParameters
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.SecureRandom
import java.security.interfaces.ECPrivateKey
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.security.spec.ECParameterSpec
import java.security.spec.ECPoint
import java.security.spec.ECPrivateKeySpec
import java.security.spec.ECPublicKeySpec
import javax.crypto.Cipher
import javax.crypto.KeyAgreement
import javax.crypto.Mac
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

/**
 * The JCE side of the push envelope's primitives. The subscription key
 * pair is an ordinary software key: herold's envelope is decrypted in a
 * woken service with no user present, which a Keystore-held key with
 * authentication bound to it could not serve, so the private scalar is
 * held in the Keystore-encrypted preference file instead
 * (`KeystoreTokenStore`).
 */

private const val CURVE = "secp256r1"
private const val UNCOMPRESSED_TAG: Byte = 4
private const val COORDINATE_LEN = 32
private const val GCM_TAG_BITS = 128

private val random = SecureRandom()

private val curveParameters: ECParameterSpec by lazy {
    val parameters = AlgorithmParameters.getInstance("EC")
    parameters.init(ECGenParameterSpec(CURVE))
    parameters.getParameterSpec(ECParameterSpec::class.java)
}

actual fun generateP256KeyPair(): P256KeyPair {
    val generator = KeyPairGenerator.getInstance("EC")
    generator.initialize(ECGenParameterSpec(CURVE), random)
    val pair = generator.generateKeyPair()
    val public = pair.public as ECPublicKey
    val private = pair.private as ECPrivateKey
    return P256KeyPair(
        publicKey = encodePoint(public.w),
        privateKey = fixedWidth(private.s, COORDINATE_LEN),
    )
}

actual fun p256SharedSecret(privateKey: ByteArray, peerPublicKey: ByteArray): ByteArray {
    val factory = KeyFactory.getInstance("EC")
    val private = factory.generatePrivate(
        ECPrivateKeySpec(BigInteger(1, privateKey), curveParameters),
    )
    val peer = factory.generatePublic(
        ECPublicKeySpec(decodePoint(peerPublicKey), curveParameters),
    )
    val agreement = KeyAgreement.getInstance("ECDH")
    agreement.init(private)
    agreement.doPhase(peer, true)
    return agreement.generateSecret()
}

actual fun hmacSha256(key: ByteArray, data: ByteArray): ByteArray {
    val mac = Mac.getInstance("HmacSHA256")
    // An all-zero HMAC key is legal and HKDF-Extract uses one when the
    // salt is absent; SecretKeySpec rejects an empty array, so a single
    // zero byte stands in for it.
    mac.init(SecretKeySpec(if (key.isEmpty()) ByteArray(1) else key, "HmacSHA256"))
    return mac.doFinal(data)
}

actual fun aesGcmOpen(key: ByteArray, nonce: ByteArray, ciphertext: ByteArray): ByteArray {
    val cipher = Cipher.getInstance("AES/GCM/NoPadding")
    cipher.init(Cipher.DECRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(GCM_TAG_BITS, nonce))
    return cipher.doFinal(ciphertext)
}

/** SEC1 uncompressed form: 0x04 || X || Y, both coordinates fixed at 32 bytes. */
private fun encodePoint(point: ECPoint): ByteArray {
    val out = ByteArray(1 + COORDINATE_LEN * 2)
    out[0] = UNCOMPRESSED_TAG
    fixedWidth(point.affineX, COORDINATE_LEN).copyInto(out, 1)
    fixedWidth(point.affineY, COORDINATE_LEN).copyInto(out, 1 + COORDINATE_LEN)
    return out
}

private fun decodePoint(encoded: ByteArray): ECPoint {
    require(encoded.size == 1 + COORDINATE_LEN * 2 && encoded[0] == UNCOMPRESSED_TAG) {
        "expected an uncompressed SEC1 P-256 point"
    }
    val x = BigInteger(1, encoded.copyOfRange(1, 1 + COORDINATE_LEN))
    val y = BigInteger(1, encoded.copyOfRange(1 + COORDINATE_LEN, encoded.size))
    return ECPoint(x, y)
}

/** A big-endian magnitude of exactly [length] bytes, left-padded or sign-byte-trimmed. */
private fun fixedWidth(value: BigInteger, length: Int): ByteArray {
    val bytes = value.toByteArray()
    return when {
        bytes.size == length -> bytes
        bytes.size > length -> bytes.copyOfRange(bytes.size - length, bytes.size)
        else -> ByteArray(length).also { bytes.copyInto(it, length - bytes.size) }
    }
}
