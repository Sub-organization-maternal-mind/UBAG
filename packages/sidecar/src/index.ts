#!/usr/bin/env node
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { pathToFileURL } from "node:url";

export interface SidecarOptions {
  gatewayBaseUrl: string | URL;
  host?: string;
  port?: number;
  allowNonLoopback?: boolean;
  fetch?: typeof fetch;
}

export interface SidecarHealth {
  service: "ubag-sidecar";
  status: "ok";
  gateway_base_url: string;
  loopback_only: boolean;
  trace_id: string;
}

const DEFAULT_HOST = "127.0.0.1";
const DEFAULT_PORT = 7878;

export function createSidecarServer(options: SidecarOptions): Server {
  const gatewayBaseUrl = normalizeBaseUrl(options.gatewayBaseUrl);
  const host = options.host ?? DEFAULT_HOST;
  assertLoopbackHost(host, options.allowNonLoopback);
  const fetchImpl = options.fetch ?? globalThis.fetch;
  if (typeof fetchImpl !== "function") {
    throw new TypeError("A fetch implementation is required to run the UBAG sidecar.");
  }

  return createServer(async (request, response) => {
    try {
      if (request.url === undefined) {
        writeJson(response, 400, errorPayload("UBAG-VALIDATION-REQUEST-001", "request URL is required"));
        return;
      }

      const route = new URL(request.url, "http://127.0.0.1");
      if (request.method === "GET" && route.pathname === "/health") {
        writeJson(response, 200, {
          service: "ubag-sidecar",
          status: "ok",
          gateway_base_url: gatewayBaseUrl,
          loopback_only: isLoopbackHost(host),
          trace_id: traceId()
        } satisfies SidecarHealth);
        return;
      }

      if (route.pathname.startsWith("/v1/")) {
        await proxyGateway(request, response, gatewayBaseUrl, fetchImpl);
        return;
      }

      writeJson(response, 404, errorPayload("UBAG-VALIDATION-ROUTE-001", "sidecar route was not found"));
    } catch (error) {
      writeJson(response, 502, errorPayload("UBAG-SIDECAR-PROXY-001", error instanceof Error ? error.message : String(error)));
    }
  });
}

export function assertLoopbackHost(host: string, allowNonLoopback = false): void {
  if (!allowNonLoopback && !isLoopbackHost(host)) {
    throw new Error("UBAG sidecar must bind to loopback unless --allow-non-loopback is explicitly set.");
  }
}

export function isLoopbackHost(host: string): boolean {
  return host === "127.0.0.1" || host === "localhost" || host === "::1";
}

async function proxyGateway(
  request: IncomingMessage,
  response: ServerResponse,
  gatewayBaseUrl: string,
  fetchImpl: typeof fetch
): Promise<void> {
  const route = parseLocalRoute(request.url ?? "/");
  const target = new URL(`${route.pathname}${route.search}`, gatewayBaseUrl);
  let body = request.method === "GET" || request.method === "HEAD" ? undefined : await readBody(request);
  const headers = new Headers();

  for (const [key, value] of Object.entries(request.headers)) {
    if (value === undefined || key.toLowerCase() === "host" || key.toLowerCase() === "content-length") {
      continue;
    }
    if (Array.isArray(value)) {
      for (const item of value) headers.append(key, item);
    } else {
      headers.set(key, value);
    }
  }

  if (requiresIdempotency(request.method ?? "GET", target.pathname) && !headers.has("Idempotency-Key")) {
    const idempotencyKey = generateIdempotencyKey();
    headers.set("Idempotency-Key", idempotencyKey);
    body = injectIdempotencyKey(body, idempotencyKey, headers);
  }

  const bodyInit = body === undefined
    ? undefined
    : (body.buffer.slice(body.byteOffset, body.byteOffset + body.byteLength) as ArrayBuffer);
  headers.set("X-Ubag-Sidecar", "loopback");

  const gatewayResponse = await fetchImpl(target, {
    method: request.method,
    headers,
    body: bodyInit
  });

  response.statusCode = gatewayResponse.status;
  gatewayResponse.headers.forEach((value, key) => {
    if (!HOP_BY_HOP_RESPONSE_HEADERS.has(key.toLowerCase())) {
      response.setHeader(key, value);
    }
  });
  response.end(Buffer.from(await gatewayResponse.arrayBuffer()));
}

const HOP_BY_HOP_RESPONSE_HEADERS = new Set([
  "connection",
  "content-encoding",
  "content-length",
  "keep-alive",
  "proxy-authenticate",
  "proxy-authorization",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade"
]);

function parseLocalRoute(rawUrl: string): URL {
  const route = new URL(rawUrl, "http://127.0.0.1");
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(rawUrl) && !isLoopbackHost(route.hostname)) {
    throw new Error("sidecar proxy only accepts relative /v1 routes or loopback absolute-form requests");
  }
  return route;
}

function requiresIdempotency(method: string, pathname: string): boolean {
  const upper = method.toUpperCase();

  if (upper === "POST") {
    return pathname === "/v1/jobs" ||
      pathname === "/v1/webhooks/replay" ||
      /^\/v1\/jobs\/[^/]+\/(cancel|retry)$/.test(pathname);
  }

  if ((upper === "PUT" || upper === "DELETE") && /^\/v1\/jobs\/[^/]+\/artifacts\/[^/]+$/.test(pathname)) {
    return true;
  }

  return false;
}

function injectIdempotencyKey(body: Buffer | undefined, idempotencyKey: string, headers: Headers): Buffer | undefined {
  if (body === undefined || body.length === 0) {
    return body;
  }

  const contentType = headers.get("content-type") ?? "";
  if (!contentType.toLowerCase().includes("application/json")) {
    return body;
  }

  try {
    const parsed = JSON.parse(body.toString("utf8"));
    if (parsed !== null && typeof parsed === "object" && !Array.isArray(parsed) && parsed.idempotency_key === undefined) {
      parsed.idempotency_key = idempotencyKey;
      return Buffer.from(JSON.stringify(parsed));
    }
  } catch {
    return body;
  }

  return body;
}

function readBody(request: IncomingMessage): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    request.on("data", (chunk) => chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)));
    request.on("end", () => resolve(Buffer.concat(chunks)));
    request.on("error", reject);
  });
}

function writeJson(response: ServerResponse, status: number, payload: unknown): void {
  response.statusCode = status;
  response.setHeader("content-type", "application/json");
  response.end(JSON.stringify(payload));
}

function errorPayload(code: string, message: string): unknown {
  return {
    error: {
      code,
      category: code.includes("SIDECAR") ? "sidecar" : "validation",
      message,
      retryable: false,
      doc_url: `https://docs.ubag.dev/errors/${code}`,
      trace_id: traceId()
    }
  };
}

function normalizeBaseUrl(baseUrl: string | URL): string {
  return new URL(baseUrl).toString().replace(/\/+$/, "/");
}

function traceId(): string {
  return `trace_${Date.now().toString(36)}${Math.random().toString(36).slice(2, 10)}`;
}

function generateIdempotencyKey(now = Date.now()): string {
  return encodeBase32(BigInt(now), 10) + encodeRandomBase32();
}

function encodeRandomBase32(): string {
  const bytes = new Uint8Array(10);
  crypto.getRandomValues(bytes);
  let value = 0n;
  for (const byte of bytes) {
    value = (value << 8n) | BigInt(byte);
  }
  return encodeBase32(value, 16).slice(-16);
}

function encodeBase32(value: bigint, minLength: number): string {
  const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
  let current = value;
  let output = "";
  do {
    output = alphabet[Number(current % 32n)] + output;
    current /= 32n;
  } while (current > 0n);
  return output.padStart(minLength, "0");
}

function parseArgs(argv: string[]): SidecarOptions {
  const options = new Map<string, string | boolean>();
  for (let index = 0; index < argv.length; index += 1) {
    const token = argv[index];
    if (!token.startsWith("--")) {
      throw new Error(`unknown argument ${token}`);
    }
    const name = token.slice(2);
    if (name === "allow-non-loopback") {
      options.set(name, true);
      continue;
    }
    const value = argv[index + 1];
    if (value === undefined) {
      throw new Error(`missing value for --${name}`);
    }
    options.set(name, value);
    index += 1;
  }

  return {
    gatewayBaseUrl: String(options.get("gateway") ?? process.env.UBAG_GATEWAY_URL ?? "http://127.0.0.1:8080"),
    host: String(options.get("host") ?? process.env.UBAG_SIDECAR_HOST ?? DEFAULT_HOST),
    port: Number(options.get("port") ?? process.env.UBAG_SIDECAR_PORT ?? DEFAULT_PORT),
    allowNonLoopback: options.get("allow-non-loopback") === true
  };
}

async function main(argv: string[]): Promise<void> {
  const options = parseArgs(argv);
  const host = options.host ?? DEFAULT_HOST;
  const port = options.port ?? DEFAULT_PORT;
  assertLoopbackHost(host, options.allowNonLoopback);

  const server = createSidecarServer(options);
  await new Promise<void>((resolve) => server.listen(port, host, resolve));
  console.log(`ubag-sidecar listening on http://${host}:${port} -> ${normalizeBaseUrl(options.gatewayBaseUrl)}`);
}

if (process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main(process.argv.slice(2)).catch((error: unknown) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  });
}
