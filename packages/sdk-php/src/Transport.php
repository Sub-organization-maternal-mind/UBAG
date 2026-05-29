<?php

declare(strict_types=1);

namespace Ubag\Sdk;

/**
 * Pluggable HTTP transport. The default implementation uses cURL; tests provide
 * a capturing implementation to assert request construction without a gateway.
 */
interface Transport
{
    /**
     * @param array{method:string,url:string,headers:array<string,string>,body:?string} $request
     * @return array{status:int,headers:array<string,string>,body:string}
     */
    public function send(array $request): array;
}
