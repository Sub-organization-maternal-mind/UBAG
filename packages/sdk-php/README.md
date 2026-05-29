# UBAG PHP SDK

Idiomatic PHP client for the UBAG v0 REST gateway. Mirrors the canonical
[`sdk-go`](../sdk-go) contract: stable `UBAG-` error codes, automatic
`Idempotency-Key` generation for mutating calls, SDK metadata headers, and a
pluggable transport for testing without a live gateway.

- API version header: `Ubag-Api-Version: 2026-05-22`
- SDK headers: `Ubag-Sdk-Name: ubag-php`, `Ubag-Sdk-Version: 0.0.0`
- Auth: `Authorization: Bearer <app-secret>`

## Requirements

- PHP >= 8.1 with `ext-json` and `ext-curl`

## Install

```bash
composer require ubag/sdk
```

## Usage

```php
use Ubag\Sdk\Client;

$client = new Client('https://gateway.example.com', [
    'app_secret' => getenv('UBAG_APP_SECRET') ?: null,
]);

$health = $client->health();

$job = $client->createJob([
    'job' => ['target' => 'mock_target', 'command_type' => 'echo'],
]);

$page = $client->listJobs(['status' => 'completed', 'limit' => 20]);

$artifact = $client->getJobArtifact($job['id'], 'report.txt');
echo $artifact['content_type'], ' ', $artifact['checksum'], PHP_EOL;
```

### Error handling

```php
use Ubag\Sdk\ApiException;
use Ubag\Sdk\TransportException;

try {
    $client->cancelJob('job_123', ['reason' => 'caller_cancelled']);
} catch (ApiException $e) {
    // $e->status, $e->code(), $e->category(), $e->retryable(), $e->traceId()
} catch (TransportException $e) {
    // network/transport failure before a response was received
}
```

### Custom transport

Implement `Ubag\Sdk\Transport` to integrate Guzzle, PSR-18, or a test double:

```php
use Ubag\Sdk\Transport;

final class GuzzleTransport implements Transport
{
    public function send(array $request): array
    {
        // return ['status' => int, 'headers' => array<string,string>, 'body' => string];
    }
}

$client = new Client('https://gateway.example.com', ['transport' => new GuzzleTransport()]);
```

## Testing

```bash
composer install
vendor/bin/phpunit
```

Tests inject a `CapturingTransport` and assert request method, path, headers,
and body without contacting a gateway. Requires network for the first
`composer install` (downloads PHPUnit); the test logic itself is offline.
