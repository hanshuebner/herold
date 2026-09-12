package com.netzhansa.herold.shared.auth

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotEquals
import kotlin.test.assertTrue

/**
 * PKCE is what binds an authorization code to this device, so its two
 * primitives are pinned to published vectors rather than to the
 * implementation's own output.
 */
class PkceTest {

    @Test
    fun sha256MatchesTheFips180Vectors() {
        assertEquals(
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
            Sha256.digest("abc".encodeToByteArray()).hex(),
        )
        assertEquals(
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
            Sha256.digest(ByteArray(0)).hex(),
        )
        assertEquals(
            "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1",
            Sha256.digest(
                ("abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq").encodeToByteArray(),
            ).hex(),
        )
        // Two blocks plus padding, the case a one-block implementation gets wrong.
        assertEquals(
            "cdc76e5c9914fb9281a1c7e284d73e67f1809a48a497200e046d39ccc7112cd0",
            Sha256.digest(ByteArray(1_000_000) { 'a'.code.toByte() }).hex(),
        )
    }

    @Test
    fun base64UrlIsUnpaddedAndUsesTheUrlAlphabet() {
        assertEquals("", Base64Url.encode(ByteArray(0)))
        assertEquals("Zg", Base64Url.encode("f".encodeToByteArray()))
        assertEquals("Zm8", Base64Url.encode("fo".encodeToByteArray()))
        assertEquals("Zm9v", Base64Url.encode("foo".encodeToByteArray()))
        // 0xFB 0xFF encodes to the two characters standard base64 spells
        // "+" and "/"; base64url spells them "-" and "_".
        assertEquals("-_8", Base64Url.encode(byteArrayOf(0xFB.toByte(), 0xFF.toByte())))
    }

    @Test
    fun theS256ChallengeMatchesRfc7636AppendixB() {
        val verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
        assertEquals("E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM", Pkce.challenge(verifier))
    }

    @Test
    fun aVerifierIsUrlSafeAndLongEnough() {
        val verifier = Pkce.verifier(ByteArray(Pkce.VERIFIER_BYTES) { it.toByte() })
        assertEquals(43, verifier.length)
        assertTrue(verifier.all { it.isLetterOrDigit() || it == '-' || it == '_' })
    }

    @Test
    fun twoVerifiersFromThePlatformRandomDiffer() {
        assertNotEquals(
            Pkce.verifier(secureRandomBytes(Pkce.VERIFIER_BYTES)),
            Pkce.verifier(secureRandomBytes(Pkce.VERIFIER_BYTES)),
        )
    }

    private fun ByteArray.hex(): String = joinToString("") {
        (it.toInt() and 0xFF).toString(16).padStart(2, '0')
    }
}
