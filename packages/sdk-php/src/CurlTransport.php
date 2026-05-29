<?php

declare(strict_types=1);

namespace Ubag\Sdk;

/** Default {@see Transport} backed by cURL. */
final class CurlTransport implements Transport
{
    public function send(array $request): array
    {
        $handle = curl_init();
        curl_setopt($handle, CURLOPT_URL, $request['url']);
        curl_setopt($handle, CURLOPT_CUSTOMREQUEST, $request['method']);
        curl_setopt($handle, CURLOPT_RETURNTRANSFER, true);
        curl_setopt($handle, CURLOPT_HEADER, true);

        $headers = [];
        foreach ($request['headers'] as $name => $value) {
            $headers[] = $name . ': ' . $value;
        }
        curl_setopt($handle, CURLOPT_HTTPHEADER, $headers);

        if ($request['body'] !== null) {
            curl_setopt($handle, CURLOPT_POSTFIELDS, $request['body']);
        }

        $raw = curl_exec($handle);
        if ($raw === false) {
            $error = curl_error($handle);
            curl_close($handle);
            throw new \RuntimeException($error);
        }

        $status = (int) curl_getinfo($handle, CURLINFO_RESPONSE_CODE);
        $headerSize = (int) curl_getinfo($handle, CURLINFO_HEADER_SIZE);
        curl_close($handle);

        $rawHeaders = substr((string) $raw, 0, $headerSize);
        $body = substr((string) $raw, $headerSize);

        $responseHeaders = [];
        foreach (preg_split('/\r\n/', $rawHeaders) ?: [] as $line) {
            $position = strpos($line, ':');
            if ($position !== false) {
                $key = strtolower(trim(substr($line, 0, $position)));
                $responseHeaders[$key] = trim(substr($line, $position + 1));
            }
        }

        return ['status' => $status, 'headers' => $responseHeaders, 'body' => $body];
    }
}
