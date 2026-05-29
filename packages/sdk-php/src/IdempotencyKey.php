<?php

declare(strict_types=1);

namespace Ubag\Sdk;

/** Generates ULID-style idempotency keys: 26 Crockford base32 characters. */
final class IdempotencyKey
{
    private const CROCKFORD_BASE32 = '0123456789ABCDEFGHJKMNPQRSTVWXYZ';

    public static function generate(?int $nowMillis = null): string
    {
        $nowMillis ??= (int) (microtime(true) * 1000);
        $key = self::encodeBase32(max($nowMillis, 0), 10);
        for ($i = 0; $i < 16; $i++) {
            $key .= self::CROCKFORD_BASE32[random_int(0, 31)];
        }

        return $key;
    }

    private static function encodeBase32(int $value, int $length): string
    {
        $buffer = array_fill(0, $length, '0');
        $remaining = $value;
        for ($i = $length - 1; $i >= 0; $i--) {
            $buffer[$i] = self::CROCKFORD_BASE32[$remaining % 32];
            $remaining = intdiv($remaining, 32);
        }

        return implode('', $buffer);
    }
}
