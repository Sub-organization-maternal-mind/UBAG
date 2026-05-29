<?php

declare(strict_types=1);

namespace Ubag\Sdk;

/** Thrown when the gateway returns a non-2xx response. */
final class ApiException extends \RuntimeException
{
    /**
     * @param array<string,string> $headers
     * @param array<string,mixed>|null $envelope
     */
    public function __construct(
        public readonly int $status,
        public readonly string $httpMethod,
        public readonly string $url,
        public readonly array $headers,
        public readonly string $rawBody,
        public readonly ?array $envelope
    ) {
        parent::__construct(self::messageFor($status, $envelope));
    }

    /** @param array<string,mixed>|null $envelope */
    private static function messageFor(int $status, ?array $envelope): string
    {
        $message = $envelope['error']['message'] ?? null;
        if (is_string($message) && $message !== '') {
            return $message;
        }

        return 'UBAG API request failed with HTTP ' . $status;
    }

    public function code(): ?string
    {
        $value = $this->envelope['error']['code'] ?? null;
        return is_string($value) ? $value : null;
    }

    public function category(): ?string
    {
        $value = $this->envelope['error']['category'] ?? null;
        return is_string($value) ? $value : null;
    }

    public function retryable(): bool
    {
        return ($this->envelope['error']['retryable'] ?? false) === true;
    }

    public function traceId(): ?string
    {
        $value = $this->envelope['error']['trace_id'] ?? null;
        if (is_string($value) && $value !== '') {
            return $value;
        }

        return $this->headers['ubag-trace-id'] ?? $this->headers['x-request-id'] ?? null;
    }
}
