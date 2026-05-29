<?php

declare(strict_types=1);

namespace Ubag\Sdk\Tests;

use PHPUnit\Framework\TestCase;
use Ubag\Sdk\ApiException;
use Ubag\Sdk\Client;
use Ubag\Sdk\Transport;

/** Capturing transport that records the last request and returns a canned response. */
final class CapturingTransport implements Transport
{
    /** @var array{method:string,url:string,headers:array<string,string>,body:?string}|null */
    public ?array $lastRequest = null;

    /** @param array<string,string> $responseHeaders */
    public function __construct(
        private readonly int $status,
        private readonly string $responseBody,
        private readonly array $responseHeaders = []
    ) {
    }

    public function send(array $request): array
    {
        $this->lastRequest = $request;
        return ['status' => $this->status, 'headers' => $this->responseHeaders, 'body' => $this->responseBody];
    }
}

final class ClientTest extends TestCase
{
    private function client(CapturingTransport $transport): Client
    {
        return new Client('http://127.0.0.1:7878/', [
            'app_secret' => 'app_secret_fixture',
            'transport' => $transport,
        ]);
    }

    public function testHealthSendsVersionAndAuthHeaders(): void
    {
        $transport = new CapturingTransport(200, '{"status":"ok"}');
        $result = $this->client($transport)->health();

        self::assertSame('ok', $result['status']);
        $request = $transport->lastRequest;
        self::assertSame('GET', $request['method']);
        self::assertSame('http://127.0.0.1:7878/v1/health', $request['url']);
        self::assertSame(Client::API_VERSION, $request['headers']['Ubag-Api-Version']);
        self::assertSame(Client::SDK_NAME, $request['headers']['Ubag-Sdk-Name']);
        self::assertSame('Bearer app_secret_fixture', $request['headers']['Authorization']);
        self::assertNull($request['body']);
    }

    public function testVersionOmitsIdempotencyKey(): void
    {
        $transport = new CapturingTransport(200, '{"version":"0.0.0"}');
        $this->client($transport)->version(['idempotency_key' => 'ignored']);

        $request = $transport->lastRequest;
        self::assertSame('http://127.0.0.1:7878/v1/version', $request['url']);
        self::assertArrayNotHasKey('Idempotency-Key', $request['headers']);
    }

    public function testCreateJobInjectsVersionIdempotencyAndSdkMetadata(): void
    {
        $transport = new CapturingTransport(202, '{"status":"queued"}');
        $body = [
            'client' => ['app_id' => 'fixture-app', 'app_version' => '0.0.0'],
            'job' => ['target' => 'mock_target', 'command_type' => 'echo'],
        ];

        $this->client($transport)->createJob($body, ['idempotency_key' => 'idem_php_sdk']);

        $request = $transport->lastRequest;
        self::assertSame('POST', $request['method']);
        self::assertSame('http://127.0.0.1:7878/v1/jobs', $request['url']);
        self::assertSame('idem_php_sdk', $request['headers']['Idempotency-Key']);
        self::assertSame('application/json', $request['headers']['Content-Type']);

        $sent = json_decode($request['body'], true);
        self::assertSame(Client::API_VERSION, $sent['api_version']);
        self::assertSame('idem_php_sdk', $sent['idempotency_key']);
        self::assertSame(Client::SDK_NAME, $sent['client']['sdk']['name']);
        self::assertSame(Client::SDK_VERSION, $sent['client']['sdk']['version']);
    }

    public function testCreateJobGeneratesIdempotencyKeyWhenMissing(): void
    {
        $transport = new CapturingTransport(202, '{"status":"queued"}');
        $this->client($transport)->createJob(['job' => ['target' => 'mock_target']]);

        $request = $transport->lastRequest;
        $key = $request['headers']['Idempotency-Key'];
        self::assertSame(26, strlen($key));
        $sent = json_decode($request['body'], true);
        self::assertSame($key, $sent['idempotency_key']);
    }

    public function testListJobsBuildsFilterQuery(): void
    {
        $transport = new CapturingTransport(200, '{"jobs":[]}');
        $this->client($transport)->listJobs(['cursor' => 'cursor_1', 'limit' => 1, 'status' => 'completed']);

        self::assertSame(
            'http://127.0.0.1:7878/v1/jobs?cursor=cursor_1&limit=1&filter%5Bstatus%5D=completed',
            $transport->lastRequest['url']
        );
    }

    public function testCancelJobIsIdempotentPost(): void
    {
        $transport = new CapturingTransport(202, '{"status":"cancelled"}');
        $this->client($transport)->cancelJob('job_1', ['reason' => 'caller_cancelled'], ['idempotency_key' => 'idem_cancel']);

        $request = $transport->lastRequest;
        self::assertSame('POST', $request['method']);
        self::assertSame('http://127.0.0.1:7878/v1/jobs/job_1/cancel', $request['url']);
        self::assertSame('idem_cancel', $request['headers']['Idempotency-Key']);
        $sent = json_decode($request['body'], true);
        self::assertSame('idem_cancel', $sent['idempotency_key']);
        self::assertSame('caller_cancelled', $sent['reason']);
    }

    public function testPutArtifactSendsBytesAndGeneratesKey(): void
    {
        $transport = new CapturingTransport(201, '{"idempotent_replay":false}');
        $this->client($transport)->putJobArtifact('job_1', 'report.txt', 'hello artifact', 'text/plain');

        $request = $transport->lastRequest;
        self::assertSame('PUT', $request['method']);
        self::assertSame('http://127.0.0.1:7878/v1/jobs/job_1/artifacts/report.txt', $request['url']);
        self::assertSame('text/plain', $request['headers']['Content-Type']);
        self::assertSame(26, strlen($request['headers']['Idempotency-Key']));
        self::assertSame('hello artifact', $request['body']);
    }

    public function testGetArtifactReturnsBytesAndChecksum(): void
    {
        $transport = new CapturingTransport(
            200,
            'hello artifact',
            ['content-type' => 'text/plain', 'ubag-artifact-checksum' => 'sha256_fixture']
        );
        $download = $this->client($transport)->getJobArtifact('job_1', 'report.txt');

        self::assertSame('hello artifact', $download['body']);
        self::assertSame('text/plain', $download['content_type']);
        self::assertSame('sha256_fixture', $download['checksum']);
    }

    public function testMetricsRequestSetsTextAccept(): void
    {
        $transport = new CapturingTransport(200, "ubag_gateway_requests_total 1\n");
        $text = $this->client($transport)->metrics();

        self::assertSame("ubag_gateway_requests_total 1\n", $text);
        self::assertSame('text/plain', $transport->lastRequest['headers']['Accept']);
    }

    public function testApiErrorEnvelopeIsParsed(): void
    {
        $transport = new CapturingTransport(
            401,
            json_encode([
                'error' => [
                    'code' => 'UBAG-AUTH-MISSING-001',
                    'category' => 'auth',
                    'message' => 'No supported credential was provided',
                    'retryable' => false,
                    'trace_id' => 'trace_auth_missing',
                ],
            ], JSON_THROW_ON_ERROR)
        );

        try {
            $this->client($transport)->listWorkflows();
            self::fail('expected ApiException');
        } catch (ApiException $error) {
            self::assertSame(401, $error->status);
            self::assertSame('UBAG-AUTH-MISSING-001', $error->code());
            self::assertSame('auth', $error->category());
            self::assertFalse($error->retryable());
            self::assertSame('trace_auth_missing', $error->traceId());
            self::assertStringContainsString('No supported credential', $error->getMessage());
        }
    }
}
