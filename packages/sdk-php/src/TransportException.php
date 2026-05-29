<?php

declare(strict_types=1);

namespace Ubag\Sdk;

/** Thrown when a request could not be sent (network/transport failure). */
final class TransportException extends \RuntimeException
{
    public function __construct(
        public readonly string $httpMethod,
        public readonly string $url,
        string $cause
    ) {
        parent::__construct(sprintf('UBAG API request could not be sent: %s %s: %s', $httpMethod, $url, $cause));
    }
}
