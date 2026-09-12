package com.netzhansa.herold.shared.push

import com.netzhansa.herold.shared.auth.Base64Url
import kotlin.test.Test
import kotlin.test.assertContentEquals
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertTrue

/**
 * The decrypt path against the RFCs' own known answers. RFC 8291
 * section 5 is the whole envelope herold produces (its Go encrypter
 * derives the same way, `internal/webpush/encrypt.go`); the surrounding
 * cases cover what a distributor can hand the client that is not a
 * well-formed envelope.
 */
class WebPushEnvelopeTest {

    // RFC 8291 section 5: "When I grow up, I want to be a watermelon"
    // encrypted to the receiver key pair below with the sender's
    // ephemeral key and salt the RFC fixes.
    private val rfc8291Body = Base64Url.decode(
        "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
            "mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
            "pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN",
    )
    private val rfc8291Keys = WebPushKeys(
        publicKey = Base64Url.decode(
            "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
        ),
        privateKey = Base64Url.decode("q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"),
        authSecret = Base64Url.decode("BTBZMqHH6r4Tts7J_aSIgg"),
    )

    @Test
    fun decryptsTheRfc8291Example() {
        assertEquals(
            "When I grow up, I want to be a watermelon",
            WebPushEnvelope.decryptToText(rfc8291Body, rfc8291Keys),
        )
    }

    @Test
    fun rejectsAnEnvelopeEncryptedForAnotherSubscription() {
        val other = WebPushKeys(
            publicKey = rfc8291Keys.publicKey,
            privateKey = rfc8291Keys.privateKey,
            authSecret = ByteArray(WebPushKeys.AUTH_SECRET_LEN) { 7 },
        )
        assertFailsWith<PushDecryptionException> { WebPushEnvelope.decrypt(rfc8291Body, other) }
    }

    @Test
    fun rejectsATamperedRecord() {
        val tampered = rfc8291Body.copyOf()
        tampered[tampered.size - 1] = (tampered[tampered.size - 1] + 1).toByte()
        assertFailsWith<PushDecryptionException> { WebPushEnvelope.decrypt(tampered, rfc8291Keys) }
    }

    @Test
    fun rejectsABodyTooShortToCarryARecord() {
        assertFailsWith<PushDecryptionException> {
            WebPushEnvelope.decrypt(rfc8291Body.copyOfRange(0, 40), rfc8291Keys)
        }
    }

    @Test
    fun rejectsAHeaderWithoutASenderKey() {
        val headerOnly = rfc8291Body.copyOf()
        headerOnly[20] = 0
        assertFailsWith<PushDecryptionException> { WebPushEnvelope.decrypt(headerOnly, rfc8291Keys) }
    }

    // RFC 8188 section 3.1: the same record framing and content-encoding
    // key derivation with the IKM supplied directly, which is what the
    // RFC 8291 derivation feeds in.
    @Test
    fun derivesTheRfc8188ContentEncryptionKey() {
        val ikm = Base64Url.decode("yqdlZ-tYemfogSmv7Ws5PQ")
        val salt = Base64Url.decode("I1BsxtFttlv3u_Oo94xnmw")
        val body = Base64Url.decode(
            "I1BsxtFttlv3u_Oo94xnmwAAEAAA-NAVub2qFgBEuQKRapoZu-IxkIva3MEB1PD-ly8Thjg",
        )
        val prk = hmacSha256(salt, ikm)
        val cek = hmacSha256(prk, "Content-Encoding: aes128gcm\u0000".encodeToByteArray() + byteArrayOf(1))
            .copyOfRange(0, 16)
        val nonce = hmacSha256(prk, "Content-Encoding: nonce\u0000".encodeToByteArray() + byteArrayOf(1))
            .copyOfRange(0, 12)
        val plaintext = aesGcmOpen(cek, nonce, body.copyOfRange(21, body.size))
        assertEquals("I am the walrus", plaintext.copyOfRange(0, plaintext.size - 1).decodeToString())
        assertEquals(2, plaintext[plaintext.size - 1].toInt())
    }

    @Test
    fun generatesAnUncompressedKeyPairAndAuthSecret() {
        val keys = WebPushKeys.generate()
        assertEquals(65, keys.publicKey.size)
        assertEquals(4, keys.publicKey[0].toInt())
        assertEquals(32, keys.privateKey.size)
        assertEquals(WebPushKeys.AUTH_SECRET_LEN, keys.authSecret.size)
        assertTrue(keys.p256dh.none { it == '+' || it == '/' || it == '=' })
    }

    @Test
    fun roundTripsThroughTheStoredForm() {
        val keys = WebPushKeys.generate()
        val restored = WebPushKeys.fromStored(keys.p256dh, keys.storedPrivateKey, keys.auth)!!
        assertContentEquals(keys.publicKey, restored.publicKey)
        assertContentEquals(keys.privateKey, restored.privateKey)
        assertContentEquals(keys.authSecret, restored.authSecret)
    }

    @Test
    fun agreesWithItselfOnAFreshKeyPair() {
        val receiver = WebPushKeys.generate()
        val sender = generateP256KeyPair()
        assertContentEquals(
            p256SharedSecret(receiver.privateKey, sender.publicKey),
            p256SharedSecret(sender.privateKey, receiver.publicKey),
        )
    }
}
