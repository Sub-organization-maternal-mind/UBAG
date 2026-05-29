package com.ubag.sdk

import java.security.SecureRandom

/** Generates ULID-style idempotency keys: 26 Crockford base32 characters. */
object IdempotencyKey {
    private const val CROCKFORD_BASE32 = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
    private val random = SecureRandom()

    fun generate(nowMillis: Long = System.currentTimeMillis()): String {
        val builder = StringBuilder(26)
        builder.append(encodeBase32(maxOf(nowMillis, 0L), 10))
        repeat(16) {
            builder.append(CROCKFORD_BASE32[random.nextInt(32)])
        }
        return builder.toString()
    }

    private fun encodeBase32(value: Long, length: Int): String {
        val buffer = CharArray(length) { '0' }
        var remaining = value
        for (i in length - 1 downTo 0) {
            buffer[i] = CROCKFORD_BASE32[(remaining % 32).toInt()]
            remaining /= 32
        }
        return String(buffer)
    }
}
