<?php

declare(strict_types=1);

namespace Ubag\Sdk;

/** PHP client for the UBAG v0 REST gateway. */
final class Client
{
    public const API_VERSION = '2026-05-22';
    public const SDK_NAME = 'ubag-php';
    public const SDK_VERSION = '0.0.0';

    private const JSON_CONTENT_TYPE = 'application/json';

    private string $baseUrl;
    private string $apiVersion;
    private ?string $appSecret;
    private Transport $transport;
    /** @var array<string,string> */
    private array $defaultHeaders;

    /**
     * @param array{app_secret?:?string,api_version?:string,transport?:Transport,headers?:array<string,string>} $options
     */
    public function __construct(string $baseUrl, array $options = [])
    {
        $baseUrl = trim($baseUrl);
        if ($baseUrl === '') {
            throw new \InvalidArgumentException('baseUrl is required');
        }
        if (!str_contains($baseUrl, '://')) {
            throw new \InvalidArgumentException('baseUrl must include scheme and host');
        }

        $this->baseUrl = rtrim($baseUrl, '/');
        $this->apiVersion = $options['api_version'] ?? self::API_VERSION;
        $this->appSecret = $options['app_secret'] ?? null;
        $this->transport = $options['transport'] ?? new CurlTransport();
        $this->defaultHeaders = $options['headers'] ?? [];
    }

    // --- System -----------------------------------------------------------

    /** @param array<string,mixed> $options */
    public function health(array $options = []): array
    {
        return $this->requestJson('GET', '/v1/health', null, $options);
    }

    /** @param array<string,mixed> $options */
    public function ready(array $options = []): array
    {
        return $this->requestJson('GET', '/v1/ready', null, $options);
    }

    /** @param array<string,mixed> $options */
    public function version(array $options = []): array
    {
        unset($options['idempotency_key']);
        return $this->requestJson('GET', '/v1/version', null, $options);
    }

    /** @param array<string,mixed> $options */
    public function metrics(array $options = []): string
    {
        $options['headers'] = ($options['headers'] ?? []) + ['Accept' => 'text/plain'];
        return $this->send('GET', '/v1/metrics', null, null, $options)['body'];
    }

    // --- Jobs --------------------------------------------------------------

    /**
     * @param array<string,mixed> $request
     * @param array<string,mixed> $options
     */
    public function createJob(array $request, array $options = []): array
    {
        $body = $request;
        $apiVersion = $this->stringField($body, 'api_version') ?? $options['api_version'] ?? $this->apiVersion;
        $idempotencyKey = $this->stringField($body, 'idempotency_key')
            ?? ($options['idempotency_key'] ?? null)
            ?? IdempotencyKey::generate();

        $body['api_version'] = $apiVersion;
        $body['idempotency_key'] = $idempotencyKey;
        $this->ensureSdkMetadata($body);

        $options['api_version'] = $apiVersion;
        $options['idempotency_key'] = $idempotencyKey;
        return $this->requestJson('POST', '/v1/jobs', $body, $options);
    }

    /** @param array<string,mixed> $options */
    public function getJob(string $jobId, array $options = []): array
    {
        return $this->requestJson('GET', '/v1/jobs/' . $this->encode($jobId), null, $options);
    }

    /**
     * @param array<string,mixed> $params
     * @param array<string,mixed> $options
     */
    public function listJobs(array $params = [], array $options = []): array
    {
        $pairs = [];
        $this->addPair($pairs, 'cursor', $params['cursor'] ?? null);
        $this->addPair($pairs, 'limit', $params['limit'] ?? null);
        $this->addPair($pairs, 'filter[status]', $params['status'] ?? null);
        $this->addPair($pairs, 'filter[target]', $params['target'] ?? null);
        $this->addPair($pairs, 'sort', $params['sort'] ?? null);
        $this->addPair($pairs, 'fields', isset($params['fields']) ? implode(',', $params['fields']) : null);
        $this->addPair($pairs, 'include', isset($params['include']) ? implode(',', $params['include']) : null);

        return $this->requestJson('GET', '/v1/jobs' . $this->encodeQuery($pairs), null, $options);
    }

    /**
     * @param array<string,mixed> $request
     * @param array<string,mixed> $options
     */
    public function cancelJob(string $jobId, array $request = [], array $options = []): array
    {
        return $this->mutate('/v1/jobs/' . $this->encode($jobId) . '/cancel', $request, $options);
    }

    /**
     * @param array<string,mixed> $request
     * @param array<string,mixed> $options
     */
    public function retryJob(string $jobId, array $request = [], array $options = []): array
    {
        return $this->mutate('/v1/jobs/' . $this->encode($jobId) . '/retry', $request, $options);
    }

    // --- Job events --------------------------------------------------------

    /**
     * @param array<string,mixed> $params
     * @param array<string,mixed> $options
     */
    public function listJobEvents(string $jobId, array $params = [], array $options = []): array
    {
        $pairs = [];
        $this->addPair($pairs, 'cursor', $params['cursor'] ?? null);
        $this->addPair($pairs, 'after_sequence', $params['after_sequence'] ?? null);
        $this->addPair($pairs, 'limit', $params['limit'] ?? null);

        return $this->requestJson(
            'GET',
            '/v1/jobs/' . $this->encode($jobId) . '/events' . $this->encodeQuery($pairs),
            null,
            $options
        );
    }

    /** @param array<string,mixed> $options */
    public function streamJobEventsSse(string $jobId, array $options = []): string
    {
        $options['headers'] = ($options['headers'] ?? []) + ['Accept' => 'text/event-stream'];
        return $this->send('GET', '/v1/sse/jobs/' . $this->encode($jobId), null, null, $options)['body'];
    }

    // --- Artifacts ---------------------------------------------------------

    /** @param array<string,mixed> $options */
    public function listJobArtifacts(string $jobId, array $options = []): array
    {
        return $this->requestJson('GET', '/v1/jobs/' . $this->encode($jobId) . '/artifacts', null, $options);
    }

    /**
     * @param array<string,mixed> $options
     * @return array{body:string,content_type:string,checksum:string}
     */
    public function getJobArtifact(string $jobId, string $key, array $options = []): array
    {
        $response = $this->send(
            'GET',
            '/v1/jobs/' . $this->encode($jobId) . '/artifacts/' . $this->encode($key),
            null,
            null,
            $options
        );

        return [
            'body' => $response['body'],
            'content_type' => $response['headers']['content-type'] ?? '',
            'checksum' => $response['headers']['ubag-artifact-checksum'] ?? '',
        ];
    }

    /** @param array<string,mixed> $options */
    public function putJobArtifact(
        string $jobId,
        string $key,
        string $body,
        string $contentType = 'application/octet-stream',
        array $options = []
    ): array {
        $options['idempotency_key'] ??= IdempotencyKey::generate();
        $resolvedType = $contentType === '' ? 'application/octet-stream' : $contentType;
        $response = $this->send(
            'PUT',
            '/v1/jobs/' . $this->encode($jobId) . '/artifacts/' . $this->encode($key),
            $body,
            $resolvedType,
            $options
        );

        return $this->decodeJson($response['body']);
    }

    /** @param array<string,mixed> $options */
    public function deleteJobArtifact(string $jobId, string $key, array $options = []): void
    {
        $options['idempotency_key'] ??= IdempotencyKey::generate();
        $this->requestJson(
            'DELETE',
            '/v1/jobs/' . $this->encode($jobId) . '/artifacts/' . $this->encode($key),
            null,
            $options
        );
    }

    // --- Operator collections ---------------------------------------------

    /** @param array<string,mixed> $options */
    public function listWorkflows(array $options = []): array
    {
        return $this->requestJson('GET', '/v1/workflows', null, $options);
    }

    /** @param array<string,mixed> $options */
    public function listTemplates(array $options = []): array
    {
        return $this->requestJson('GET', '/v1/templates', null, $options);
    }

    /**
     * @param array<string,mixed> $params
     * @param array<string,mixed> $options
     */
    public function listTargets(array $params = [], array $options = []): array
    {
        return $this->requestJson('GET', '/v1/targets' . $this->buildListQuery($params), null, $options);
    }

    /**
     * @param array<string,mixed> $params
     * @param array<string,mixed> $options
     */
    public function listAdapters(array $params = [], array $options = []): array
    {
        return $this->requestJson('GET', '/v1/adapters' . $this->buildListQuery($params), null, $options);
    }

    /**
     * @param array<string,mixed> $params
     * @param array<string,mixed> $options
     */
    public function listApps(array $params = [], array $options = []): array
    {
        return $this->requestJson('GET', '/v1/apps' . $this->buildListQuery($params), null, $options);
    }

    /**
     * @param array<string,mixed> $params
     * @param array<string,mixed> $options
     */
    public function listDevices(array $params = [], array $options = []): array
    {
        return $this->requestJson('GET', '/v1/devices' . $this->buildListQuery($params), null, $options);
    }

    /**
     * @param array<string,mixed> $params
     * @param array<string,mixed> $options
     */
    public function listWebhooks(array $params = [], array $options = []): array
    {
        return $this->requestJson('GET', '/v1/webhooks' . $this->buildListQuery($params), null, $options);
    }

    /**
     * @param array<string,mixed> $params
     * @param array<string,mixed> $options
     */
    public function listAuditEvents(array $params = [], array $options = []): array
    {
        return $this->requestJson('GET', '/v1/audit' . $this->buildListQuery($params), null, $options);
    }

    /**
     * @param array<string,mixed> $params
     * @param array<string,mixed> $options
     */
    public function listEvents(array $params = [], array $options = []): array
    {
        return $this->requestJson('GET', '/v1/events' . $this->buildListQuery($params), null, $options);
    }

    // --- Webhook replay & cache -------------------------------------------

    /**
     * @param array<string,mixed> $request
     * @param array<string,mixed> $options
     */
    public function replayWebhookDelivery(array $request = [], array $options = []): array
    {
        return $this->mutate('/v1/webhooks/replay', $request, $options);
    }

    /** @param array<string,mixed> $options */
    public function cacheStatus(array $options = []): array
    {
        return $this->requestJson('GET', '/v1/cache', null, $options);
    }

    // --- Internal helpers --------------------------------------------------

    /**
     * @param array<string,mixed> $request
     * @param array<string,mixed> $options
     */
    private function mutate(string $path, array $request, array $options): array
    {
        $body = $request;
        $apiVersion = $this->stringField($body, 'api_version') ?? $options['api_version'] ?? $this->apiVersion;
        $idempotencyKey = $this->stringField($body, 'idempotency_key')
            ?? ($options['idempotency_key'] ?? null)
            ?? IdempotencyKey::generate();

        $body['api_version'] = $apiVersion;
        $body['idempotency_key'] = $idempotencyKey;
        $options['api_version'] = $apiVersion;
        $options['idempotency_key'] = $idempotencyKey;

        return $this->requestJson('POST', $path, $body, $options);
    }

    /**
     * @param array<string,mixed>|null $body
     * @param array<string,mixed> $options
     */
    private function requestJson(string $method, string $path, ?array $body, array $options): array
    {
        $serialized = $body === null ? null : json_encode($body, JSON_THROW_ON_ERROR | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE);
        $contentType = $serialized === null ? null : self::JSON_CONTENT_TYPE;
        $response = $this->send($method, $path, $serialized, $contentType, $options);
        if ($response['body'] === '' || $response['status'] === 204) {
            return [];
        }

        return $this->decodeJson($response['body']);
    }

    /**
     * @param array<string,mixed> $options
     * @return array{status:int,headers:array<string,string>,body:string}
     */
    private function send(string $method, string $path, ?string $body, ?string $contentType, array $options): array
    {
        $url = $this->baseUrl . $path;
        $apiVersion = $options['api_version'] ?? $this->apiVersion;

        $headers = [
            'Accept' => self::JSON_CONTENT_TYPE,
            'Ubag-Api-Version' => $apiVersion,
            'Ubag-Sdk-Name' => self::SDK_NAME,
            'Ubag-Sdk-Version' => self::SDK_VERSION,
        ];
        $headers = array_merge($headers, $this->defaultHeaders, $options['headers'] ?? []);
        if ($this->appSecret !== null && !isset($headers['Authorization'])) {
            $headers['Authorization'] = 'Bearer ' . $this->appSecret;
        }
        if (isset($options['idempotency_key'])) {
            $headers['Idempotency-Key'] = $options['idempotency_key'];
        }
        if ($body !== null) {
            $headers['Content-Type'] = $contentType ?? self::JSON_CONTENT_TYPE;
        }

        try {
            $response = $this->transport->send([
                'method' => $method,
                'url' => $url,
                'headers' => $headers,
                'body' => $body,
            ]);
        } catch (\Throwable $cause) {
            throw new TransportException($method, $url, $cause->getMessage());
        }

        if ($response['status'] < 200 || $response['status'] >= 300) {
            throw new ApiException(
                $response['status'],
                $method,
                $url,
                $response['headers'] ?? [],
                $response['body'],
                $this->parseEnvelope($response['body'])
            );
        }

        return $response;
    }

    /** @return array<string,mixed>|null */
    private function parseEnvelope(string $body): ?array
    {
        if ($body === '') {
            return null;
        }
        try {
            $parsed = json_decode($body, true, 512, JSON_THROW_ON_ERROR);
        } catch (\JsonException) {
            return null;
        }
        if (is_array($parsed) && isset($parsed['error']['code']) && is_string($parsed['error']['code'])
            && str_starts_with($parsed['error']['code'], 'UBAG-')) {
            return $parsed;
        }

        return null;
    }

    /** @return array<string,mixed> */
    private function decodeJson(string $body): array
    {
        $parsed = json_decode($body, true, 512, JSON_THROW_ON_ERROR);
        return is_array($parsed) ? $parsed : [];
    }

    /** @param array<string,mixed> $body */
    private function ensureSdkMetadata(array &$body): void
    {
        if (!isset($body['client']) || !is_array($body['client'])) {
            $body['client'] = [];
        }
        if (!isset($body['client']['sdk'])) {
            $body['client']['sdk'] = ['name' => self::SDK_NAME, 'version' => self::SDK_VERSION];
        }
    }

    /** @param array<string,mixed> $body */
    private function stringField(array $body, string $key): ?string
    {
        $value = $body[$key] ?? null;
        return is_string($value) && $value !== '' ? $value : null;
    }

    /**
     * @param array<int,array{string,string}> $pairs
     */
    private function addPair(array &$pairs, string $key, mixed $value): void
    {
        if ($value === null || $value === '') {
            return;
        }
        if (is_int($value) && $value <= 0) {
            return;
        }
        $pairs[] = [$key, (string) $value];
    }

    /** @param array<string,mixed> $params */
    private function buildListQuery(array $params): string
    {
        $pairs = [];
        $this->addPair($pairs, 'cursor', $params['cursor'] ?? null);
        $this->addPair($pairs, 'limit', $params['limit'] ?? null);
        return $this->encodeQuery($pairs);
    }

    /** @param array<int,array{string,string}> $pairs */
    private function encodeQuery(array $pairs): string
    {
        if ($pairs === []) {
            return '';
        }
        $encoded = array_map(
            static fn (array $pair): string => rawurlencode($pair[0]) . '=' . rawurlencode($pair[1]),
            $pairs
        );

        return '?' . implode('&', $encoded);
    }

    private function encode(string $value): string
    {
        return rawurlencode($value);
    }
}
