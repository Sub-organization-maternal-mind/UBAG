using System.Security.Cryptography;

namespace Ubag.Sdk;

/// <summary>Generates ULID-style idempotency keys: 26 Crockford base32 characters.</summary>
public static class IdempotencyKey
{
    private const string CrockfordBase32 = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";

    public static string Generate()
    {
        var nowMillis = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds();
        Span<char> buffer = stackalloc char[26];
        EncodeBase32(nowMillis, buffer[..10]);

        Span<byte> random = stackalloc byte[16];
        RandomNumberGenerator.Fill(random);
        for (var i = 0; i < 16; i++)
        {
            buffer[10 + i] = CrockfordBase32[random[i] & 31];
        }

        return new string(buffer);
    }

    private static void EncodeBase32(long value, Span<char> target)
    {
        var remaining = value < 0 ? 0 : value;
        for (var i = target.Length - 1; i >= 0; i--)
        {
            target[i] = CrockfordBase32[(int)(remaining % 32)];
            remaining /= 32;
        }
    }
}
