package com.ubag.sdk;

import java.security.SecureRandom;

/**
 * Generates ULID-style idempotency keys: 26 characters of Crockford base32,
 * a 10-character millisecond timestamp followed by 16 characters of entropy.
 */
public final class IdempotencyKey {

    private static final char[] CROCKFORD_BASE32 = "0123456789ABCDEFGHJKMNPQRSTVWXYZ".toCharArray();
    private static final SecureRandom RANDOM = new SecureRandom();

    private IdempotencyKey() {
    }

    public static String generate() {
        return generate(System.currentTimeMillis());
    }

    static String generate(long nowMillis) {
        StringBuilder builder = new StringBuilder(26);
        builder.append(encodeBase32(Math.max(nowMillis, 0L), 10));
        for (int i = 0; i < 16; i++) {
            builder.append(CROCKFORD_BASE32[RANDOM.nextInt(32)]);
        }
        return builder.toString();
    }

    private static String encodeBase32(long value, int length) {
        char[] buffer = new char[length];
        long remaining = value;
        for (int i = length - 1; i >= 0; i--) {
            buffer[i] = CROCKFORD_BASE32[(int) (remaining % 32)];
            remaining /= 32;
        }
        return new String(buffer);
    }
}
