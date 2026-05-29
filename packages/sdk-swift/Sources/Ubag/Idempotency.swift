import Foundation

/// Generates ULID-style idempotency keys: 26 Crockford base32 characters.
public enum IdempotencyKey {
    private static let alphabet = Array("0123456789ABCDEFGHJKMNPQRSTVWXYZ")

    public static func generate(nowMillis: Int64? = nil) -> String {
        let timestamp = nowMillis ?? Int64(Date().timeIntervalSince1970 * 1000)
        var characters = encodeBase32(max(timestamp, 0), length: 10)
        for _ in 0..<16 {
            characters.append(alphabet[Int.random(in: 0..<32)])
        }
        return String(characters)
    }

    private static func encodeBase32(_ value: Int64, length: Int) -> [Character] {
        var buffer = [Character](repeating: "0", count: length)
        var remaining = value
        var index = length - 1
        while index >= 0 {
            buffer[index] = alphabet[Int(remaining % 32)]
            remaining /= 32
            index -= 1
        }
        return buffer
    }
}
