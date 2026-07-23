#!/usr/bin/env node
import { spawn } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { basename, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  UBAG_DEFAULT_API_VERSION,
  UbagApiError,
  UbagTransportError,
  createUbagClient,
  generateIdempotencyKey,
  type UbagAlertActionRequest,
  type UbagAttachmentKind,
  type UbagAttachmentUpload,
  type UbagAuditExportRange,
  type UbagAuditExportRequest,
  type UbagClientOptions,
  type UbagCreateJobRequest,
  type UbagJobCommand,
  type UbagJobMutationRequest,
  type UbagJobOptions,
  type UbagJsonObject,
  type UbagListEventsParams,
  type UbagListJobsParams,
  type UbagSsoLogoutRequest
} from "@ubag/sdk";

const CLI_VERSION = "0.0.0";
const DEFAULT_BASE_URL = "http://localhost:8080";
const DEFAULT_APP_SECRET = "dev-secret";

const PACKAGE_ROOT = fileURLToPath(new URL("..", import.meta.url));
const REPO_ROOT = resolve(PACKAGE_ROOT, "..", "..");
const MOCK_WORKER_SCRIPT = resolve(REPO_ROOT, "apps", "worker", "run_mock_worker.py");

const COMMANDS = new Set([
  "help",
  "health",
  "ready",
  "version",
  "diagnose",
  "create-job",
  "get-job",
  "list-jobs",
  "list-workflows",
  "list-templates",
  "list-job-events",
  "list-events",
  "list-apps",
  "list-devices",
  "list-audit-events",
  "list-targets",
  "list-adapters",
  "list-webhooks",
  "list-artifacts",
  "put-artifact",
  "get-artifact",
  "delete-artifact",
  "replay-webhook",
  "cache-status",
  "metrics",
  "cancel-job",
  "retry-job",
  "list-alerts",
  "alert-config",
  "acknowledge-alert",
  "resolve-alert",
  "list-browser-instances",
  "list-browser-contexts",
  "list-browser-tabs",
  "browser-summary",
  "list-concurrency",
  "sso-logout",
  "export-audit",
  "stream",
  "stream-sse",
  "mock-run",
  "adapter-test"
]);
const BOOLEAN_OPTIONS = new Set(["help", "json", "pretty", "no-auth", "raw"]);
const VALUE_OPTIONS = new Set([
  "api-version",
  "app-secret",
  "after-sequence",
  "attach",
  "base-url",
  "client-app-id",
  "command-type",
  "content-type",
  "conversation",
  "conversation-missing",
  "cursor",
  "delivery-id",
  "context-id",
  "fields",
  "file",
  "from-sequence",
  "idempotency-key",
  "include",
  "input",
  "input-json",
  "instance-id",
  "limit",
  "max-events",
  "model",
  "options-json",
  "output",
  "payload",
  "prompt",
  "python",
  "reason",
  "scope-app-id",
  "since",
  "sort",
  "state",
  "status",
  "target",
  "tenant-id",
  "thinking",
  "timeout-ms",
  "to-sequence",
  "until"
]);

const OPTION_ALIASES = new Map([
  ["h", "help"],
  ["i", "input"],
  ["o", "output"]
]);

interface ParsedArgs {
  command: string | undefined;
  options: Map<string, string>;
  flags: Set<string>;
  positionals: string[];
}

interface RuntimeConfig {
  baseUrl: string;
  apiVersion: string;
  appSecret: string | undefined;
  headers: Record<string, string>;
}

interface SseEvent {
  event?: string;
  data?: unknown;
  rawData?: string;
  id?: string;
  retry?: string;
}

async function main(argv: string[]): Promise<number> {
  const args = parseCli(argv);

  if (args.command === undefined || hasFlag(args, "help")) {
    printHelp(args.command ?? args.positionals[0]);
    return 0;
  }

  if (!COMMANDS.has(args.command)) {
    throw new CliUsageError(`unknown command "${args.command}"`);
  }

  switch (args.command) {
    case "help":
      printHelp(args.positionals[0]);
      return 0;
    case "health":
      await runHealth(args);
      return 0;
    case "ready":
      await runReady(args);
      return 0;
    case "version":
      await runVersion(args);
      return 0;
    case "diagnose":
      await runDiagnose(args);
      return 0;
    case "create-job":
      await runCreateJob(args);
      return 0;
    case "get-job":
      await runGetJob(args);
      return 0;
    case "list-jobs":
      await runListJobs(args);
      return 0;
    case "list-workflows":
      await runListWorkflows(args);
      return 0;
    case "list-templates":
      await runListTemplates(args);
      return 0;
    case "list-job-events":
      await runListJobEvents(args);
      return 0;
    case "list-events":
      await runListEvents(args);
      return 0;
    case "list-apps":
      await runListApps(args);
      return 0;
    case "list-devices":
      await runListDevices(args);
      return 0;
    case "list-audit-events":
      await runListAuditEvents(args);
      return 0;
    case "list-targets":
      await runListTargets(args);
      return 0;
    case "list-adapters":
      await runListAdapters(args);
      return 0;
    case "list-webhooks":
      await runListWebhooks(args);
      return 0;
    case "list-artifacts":
      await runListArtifacts(args);
      return 0;
    case "put-artifact":
      await runPutArtifact(args);
      return 0;
    case "get-artifact":
      await runGetArtifact(args);
      return 0;
    case "delete-artifact":
      await runDeleteArtifact(args);
      return 0;
    case "replay-webhook":
      await runReplayWebhook(args);
      return 0;
    case "cache-status":
      await runCacheStatus(args);
      return 0;
    case "metrics":
      await runMetrics(args);
      return 0;
    case "cancel-job":
      await runCancelJob(args);
      return 0;
    case "retry-job":
      await runRetryJob(args);
      return 0;
    case "list-alerts":
      await runListAlerts(args);
      return 0;
    case "alert-config":
      await runAlertConfig(args);
      return 0;
    case "acknowledge-alert":
      await runAcknowledgeAlert(args);
      return 0;
    case "resolve-alert":
      await runResolveAlert(args);
      return 0;
    case "list-browser-instances":
      await runListBrowserInstances(args);
      return 0;
    case "list-browser-contexts":
      await runListBrowserContexts(args);
      return 0;
    case "list-browser-tabs":
      await runListBrowserTabs(args);
      return 0;
    case "browser-summary":
      await runBrowserSummary(args);
      return 0;
    case "list-concurrency":
      await runListConcurrency(args);
      return 0;
    case "sso-logout":
      await runSsoLogout(args);
      return 0;
    case "export-audit":
      await runExportAudit(args);
      return 0;
    case "stream":
    case "stream-sse":
      await runStreamSse(args);
      return 0;
    case "mock-run":
      return runMockWorker(args);
    case "adapter-test":
      return runAdapterTest(args);
    default:
      throw new CliUsageError(`unknown command "${args.command}"`);
  }
}

async function runHealth(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const response = await client.health();
  printJson(response, args);
}

async function runReady(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const response = await client.ready();
  printJson(response, args);
}

async function runVersion(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const response = await client.version();
  printJson(response, args);
}

async function runDiagnose(args: ParsedArgs): Promise<void> {
  const { client, config } = buildClient(args);
  let health: unknown;
  try {
    health = await client.health();
  } catch (error) {
    health = {
      status: "unreachable",
      message: error instanceof Error ? error.message : String(error)
    };
  }

  printJson(
    {
      cli_version: CLI_VERSION,
      base_url: config.baseUrl,
      api_version: config.apiVersion,
      auth_configured: config.appSecret !== undefined,
      tenant_scoped: config.headers["Ubag-Tenant-Id"] !== undefined,
      app_scoped: config.headers["Ubag-App-Id"] !== undefined,
      gateway_health: health
    },
    args
  );
}

const ATTACH_CONTENT_TYPES: Record<string, string> = {
  pdf: "application/pdf",
  txt: "text/plain",
  md: "text/markdown",
  csv: "text/csv",
  json: "application/json",
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  webm: "audio/webm",
  wav: "audio/wav",
  mp3: "audio/mpeg",
  m4a: "audio/mp4",
  ogg: "audio/ogg",
  mp4: "video/mp4"
};

function attachmentKindForContentType(contentType: string): UbagAttachmentKind {
  if (contentType.startsWith("image/")) return "image";
  if (contentType.startsWith("audio/")) return "voice";
  if (contentType.startsWith("video/")) return "video";
  return "document";
}

function buildAttachmentsFromSpec(spec: string): UbagAttachmentUpload[] {
  return spec
    .split(",")
    .map((entry) => entry.trim())
    .filter((entry) => entry.length > 0)
    .map((path) => {
      const body = readFileSync(resolve(path));
      const key = basename(path);
      const ext = key.toLowerCase().split(".").pop() ?? "";
      const content_type = ATTACH_CONTENT_TYPES[ext] ?? "application/octet-stream";
      return { key, filename: key, content_type, kind: attachmentKindForContentType(content_type), body };
    });
}

async function runCreateJob(args: ParsedArgs): Promise<void> {
  const { client, config } = buildClient(args);
  const request = buildCreateJobRequest(args, config);
  const idempotencyKey = request.idempotency_key ?? getOption(args, "idempotency-key");
  const requestOptions = idempotencyKey === undefined ? {} : { idempotencyKey };

  const attachSpec = getOption(args, "attach");
  if (attachSpec !== undefined && attachSpec.trim() !== "") {
    const attachments = buildAttachmentsFromSpec(attachSpec);
    // One-shot multipart create: envelope + files in a single request.
    const response = await client.createJobMultipart(request, attachments, requestOptions);
    printJson(response, args);
    return;
  }

  const response = await client.createJob(request, requestOptions);
  printJson(response, args);
}

async function runGetJob(args: ParsedArgs): Promise<void> {
  const jobId = requirePositional(args, 0, "job_id");
  const { client } = buildClient(args);
  const response = await client.getJob(jobId);
  printJson(response, args);
}

async function runListJobs(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const params = buildListParams(args);
  const response = await client.listJobs(params);
  printJson(response, args);
}

async function runListWorkflows(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const response = await client.listWorkflows();
  printJson(response, args);
}

async function runListTemplates(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const response = await client.listTemplates();
  printJson(response, args);
}

async function runListJobEvents(args: ParsedArgs): Promise<void> {
  const jobId = requirePositional(args, 0, "job_id");
  const { client } = buildClient(args);
  const response = await client.listJobEvents(jobId, buildJobEventsParams(args));
  printJson(response, args);
}

async function runListEvents(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const response = await client.listEvents(buildListEventsParams(args));
  printJson(response, args);
}

async function runListApps(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const response = await client.listApps(buildListEventsParams(args));
  printJson(response, args);
}

async function runListDevices(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const response = await client.listDevices(buildListEventsParams(args));
  printJson(response, args);
}

async function runListAuditEvents(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const response = await client.listAuditEvents(buildListEventsParams(args));
  printJson(response, args);
}

async function runListTargets(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const response = await client.listTargets(buildListEventsParams(args));
  printJson(response, args);
}

async function runListAdapters(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const response = await client.listAdapters(buildListEventsParams(args));
  printJson(response, args);
}

async function runListWebhooks(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const response = await client.listWebhooks(buildListEventsParams(args));
  printJson(response, args);
}

async function runListArtifacts(args: ParsedArgs): Promise<void> {
  const jobId = requirePositional(args, 0, "job_id");
  const { client } = buildClient(args);
  const response = await client.listJobArtifacts(jobId);
  printJson(response, args);
}

async function runPutArtifact(args: ParsedArgs): Promise<void> {
  const jobId = requirePositional(args, 0, "job_id");
  const key = requirePositional(args, 1, "key");
  const file = requireString(getOption(args, "file"), "file");
  const { client } = buildClient(args);
  const response = await client.putJobArtifact(jobId, key, new Blob([readFileSync(resolve(file))]), {
    contentType: getOption(args, "content-type") ?? "application/octet-stream",
    idempotencyKey: getOption(args, "idempotency-key") ?? generateIdempotencyKey()
  });
  printJson(response, args);
}

async function runGetArtifact(args: ParsedArgs): Promise<void> {
  const jobId = requirePositional(args, 0, "job_id");
  const key = requirePositional(args, 1, "key");
  const { client } = buildClient(args);
  const response = await client.getJobArtifact(jobId, key);
  const output = getOption(args, "output");
  if (output !== undefined) {
    writeFileSync(resolve(output), Buffer.from(response.body));
    printJson({ job_id: jobId, key, output, content_type: response.content_type, checksum: response.checksum }, args);
    return;
  }
  printJson(
    {
      job_id: jobId,
      key,
      content_type: response.content_type,
      checksum: response.checksum,
      body_base64: Buffer.from(response.body).toString("base64")
    },
    args
  );
}

async function runDeleteArtifact(args: ParsedArgs): Promise<void> {
  const jobId = requirePositional(args, 0, "job_id");
  const key = requirePositional(args, 1, "key");
  const { client } = buildClient(args);
  await client.deleteJobArtifact(jobId, key, {
    idempotencyKey: getOption(args, "idempotency-key") ?? generateIdempotencyKey()
  });
  printJson({ deleted: true, job_id: jobId, key }, args);
}

async function runReplayWebhook(args: ParsedArgs): Promise<void> {
  const { client, config } = buildClient(args);
  const idempotencyKey = getOption(args, "idempotency-key") ?? generateIdempotencyKey();
  const response = await client.replayWebhookDelivery(buildWebhookReplayRequest(args, config, idempotencyKey), {
    idempotencyKey
  });
  printJson(response, args);
}

async function runCacheStatus(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const response = await client.cacheStatus();
  printJson(response, args);
}

async function runMetrics(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const response = await client.getMetrics();
  if (hasFlag(args, "raw")) {
    process.stdout.write(response.endsWith("\n") ? response : `${response}\n`);
    return;
  }
  printJson({ format: "prometheus", body: response }, args);
}

async function runCancelJob(args: ParsedArgs): Promise<void> {
  const jobId = requirePositional(args, 0, "job_id");
  const { client, config } = buildClient(args);
  const idempotencyKey = getOption(args, "idempotency-key") ?? generateIdempotencyKey();
  const response = await client.cancelJob(jobId, buildMutationRequest(args, config, idempotencyKey), {
    idempotencyKey
  });
  printJson(response, args);
}

async function runRetryJob(args: ParsedArgs): Promise<void> {
  const jobId = requirePositional(args, 0, "job_id");
  const { client, config } = buildClient(args);
  const idempotencyKey = getOption(args, "idempotency-key") ?? generateIdempotencyKey();
  const response = await client.retryJob(jobId, buildMutationRequest(args, config, idempotencyKey), {
    idempotencyKey
  });
  printJson(response, args);
}

async function runListAlerts(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const params: Parameters<typeof client.listAlerts>[0] = {};
  const limit = getOption(args, "limit");
  const status = getOption(args, "status");
  if (limit !== undefined) {
    params.limit = parsePositiveInt(limit, "limit");
  }
  if (status !== undefined) {
    params.status = status;
  }
  const response = await client.listAlerts(params);
  printJson(response, args);
}

async function runAlertConfig(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const response = await client.getAlertConfig();
  printJson(response, args);
}

async function runAcknowledgeAlert(args: ParsedArgs): Promise<void> {
  const alertId = requirePositional(args, 0, "alert_id");
  const { client, config } = buildClient(args);
  const idempotencyKey = getOption(args, "idempotency-key") ?? generateIdempotencyKey();
  const response = await client.acknowledgeAlert(alertId, buildAlertActionRequest(args, config, idempotencyKey), {
    idempotencyKey
  });
  printJson(response, args);
}

async function runResolveAlert(args: ParsedArgs): Promise<void> {
  const alertId = requirePositional(args, 0, "alert_id");
  const { client, config } = buildClient(args);
  const idempotencyKey = getOption(args, "idempotency-key") ?? generateIdempotencyKey();
  const response = await client.resolveAlert(alertId, buildAlertActionRequest(args, config, idempotencyKey), {
    idempotencyKey
  });
  printJson(response, args);
}

async function runListBrowserInstances(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const params: Parameters<typeof client.listBrowserInstances>[0] = {};
  const limit = getOption(args, "limit");
  const state = getOption(args, "state");
  if (limit !== undefined) {
    params.limit = parsePositiveInt(limit, "limit");
  }
  if (state !== undefined) {
    params.state = state;
  }
  const response = await client.listBrowserInstances(params);
  printJson(response, args);
}

async function runListBrowserContexts(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const params: Parameters<typeof client.listProviderContexts>[0] = {};
  const limit = getOption(args, "limit");
  const instanceId = getOption(args, "instance-id");
  if (limit !== undefined) {
    params.limit = parsePositiveInt(limit, "limit");
  }
  if (instanceId !== undefined) {
    params.instance_id = instanceId;
  }
  const response = await client.listProviderContexts(params);
  printJson(response, args);
}

async function runListBrowserTabs(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const params: Parameters<typeof client.listBrowserTabs>[0] = {};
  const limit = getOption(args, "limit");
  const contextId = getOption(args, "context-id");
  const state = getOption(args, "state");
  if (limit !== undefined) {
    params.limit = parsePositiveInt(limit, "limit");
  }
  if (contextId !== undefined) {
    params.context_id = contextId;
  }
  if (state !== undefined) {
    params.state = state;
  }
  const response = await client.listBrowserTabs(params);
  printJson(response, args);
}

async function runBrowserSummary(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const response = await client.getBrowserTopologySummary();
  printJson(response, args);
}

async function runListConcurrency(args: ParsedArgs): Promise<void> {
  const { client } = buildClient(args);
  const params: Parameters<typeof client.getConcurrency>[0] = {};
  const cursor = getOption(args, "cursor");
  const limit = getOption(args, "limit");
  if (cursor !== undefined) {
    params.cursor = cursor;
  }
  if (limit !== undefined) {
    params.limit = parsePositiveInt(limit, "limit");
  }
  const response = await client.getConcurrency(params);
  printJson(response, args);
}

async function runSsoLogout(args: ParsedArgs): Promise<void> {
  const { client, config } = buildClient(args);
  const idempotencyKey = getOption(args, "idempotency-key") ?? generateIdempotencyKey();
  const response = await client.ssoLogout(buildSsoLogoutRequest(args, config, idempotencyKey), {
    idempotencyKey
  });
  printJson(response, args);
}

async function runExportAudit(args: ParsedArgs): Promise<void> {
  const { client, config } = buildClient(args);
  const idempotencyKey = getOption(args, "idempotency-key") ?? generateIdempotencyKey();
  const response = await client.exportAudit(buildAuditExportRequest(args, config, idempotencyKey), {
    idempotencyKey
  });
  printJson(response, args);
}

async function runStreamSse(args: ParsedArgs): Promise<void> {
  const jobId = requirePositional(args, 0, "job_id");
  const { config } = buildClient(args);
  const timeoutMs = parsePositiveInt(getOption(args, "timeout-ms") ?? "10000", "timeout-ms");
  const maxEvents = parsePositiveInt(getOption(args, "max-events") ?? "1", "max-events");
  const url = new URL(`/v1/sse/jobs/${encodeURIComponent(jobId)}`, normalizeBaseUrl(config.baseUrl));
  const headers = new Headers({
    Accept: "text/event-stream",
    "Ubag-Api-Version": config.apiVersion,
    ...config.headers
  });

  if (config.appSecret !== undefined) {
    headers.set("Authorization", `Bearer ${config.appSecret}`);
  }

  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), timeoutMs);

  try {
    const response = await fetch(url, {
      headers,
      signal: controller.signal
    });

    if (!response.ok) {
      throw await buildHttpError(response, url.toString(), "GET");
    }

    const text = await readBoundedEventStream(response, maxEvents);
    if (hasFlag(args, "raw")) {
      process.stdout.write(text.endsWith("\n") ? text : `${text}\n`);
      return;
    }

    printJson(
      {
        mode: "sse-snapshot",
        safe: true,
        endpoint: `/v1/sse/jobs/${jobId}`,
        timeout_ms: timeoutMs,
        max_events: maxEvents,
        events: parseSseEvents(text)
      },
      args
    );
  } catch (error) {
    if (isAbortError(error)) {
      throw new CliRuntimeError(`SSE read timed out after ${timeoutMs} ms`);
    }
    throw error;
  } finally {
    clearTimeout(timeout);
  }
}

async function runMockWorker(args: ParsedArgs): Promise<number> {
  const { config } = buildClient(args);
  const python = getOption(args, "python") ?? process.env.UBAG_PYTHON ?? "python";
  const workerArgs = [MOCK_WORKER_SCRIPT];
  const payload = getOption(args, "payload");
  const input = getOption(args, "input");
  const output = getOption(args, "output");

  if (payload !== undefined && input !== undefined) {
    throw new CliUsageError("mock-run accepts either --payload or --input, not both");
  }

  if (payload !== undefined) {
    workerArgs.push("--payload", payload);
  } else if (input !== undefined) {
    workerArgs.push("--input", input);
  } else {
    workerArgs.push("--payload", JSON.stringify(defaultMockWorkerPayload(args, config)));
  }

  if (output !== undefined) {
    workerArgs.push("--output", output);
  }

  const child = spawn(python, workerArgs, {
    cwd: REPO_ROOT,
    stdio: "inherit",
    windowsHide: true
  });

  return new Promise((resolvePromise, reject) => {
    child.once("error", reject);
    child.once("close", (code) => resolvePromise(code ?? 1));
  });
}

async function runAdapterTest(args: ParsedArgs): Promise<number> {
  const target = getOption(args, "target") ?? "mock";
  if (target !== "mock") {
    throw new CliUsageError("adapter-test currently executes the safe local mock adapter; use --target mock");
  }
  return runMockWorker(args);
}

function buildClient(args: ParsedArgs): { client: ReturnType<typeof createUbagClient>; config: RuntimeConfig } {
  const config = buildRuntimeConfig(args);
  const options: UbagClientOptions = {
    baseUrl: config.baseUrl,
    apiVersion: config.apiVersion,
    headers: config.headers
  };

  if (config.appSecret !== undefined) {
    options.appSecret = config.appSecret;
  }

  return {
    client: createUbagClient(options),
    config
  };
}

function buildRuntimeConfig(args: ParsedArgs): RuntimeConfig {
  const baseUrl = getOption(args, "base-url") ?? process.env.UBAG_BASE_URL ?? process.env.UBAG_GATEWAY_URL ?? DEFAULT_BASE_URL;
  const apiVersion = getOption(args, "api-version") ?? process.env.UBAG_API_VERSION ?? UBAG_DEFAULT_API_VERSION;
  const appSecret = hasFlag(args, "no-auth") ? undefined : getOption(args, "app-secret") ?? process.env.UBAG_APP_SECRET ?? DEFAULT_APP_SECRET;
  const headers: Record<string, string> = {};
  const tenantId = getOption(args, "tenant-id");
  const scopeAppId = getOption(args, "scope-app-id");

  if (tenantId !== undefined) {
    headers["Ubag-Tenant-Id"] = tenantId;
  }
  if (scopeAppId !== undefined) {
    headers["Ubag-App-Id"] = scopeAppId;
  }

  return {
    baseUrl,
    apiVersion,
    appSecret,
    headers
  };
}

function buildCreateJobRequest(args: ParsedArgs, config: RuntimeConfig): UbagCreateJobRequest {
  const payload = getOption(args, "payload");
  const file = getOption(args, "file");

  if (payload !== undefined && file !== undefined) {
    throw new CliUsageError("create-job accepts either --payload or --file, not both");
  }

  if (payload !== undefined || file !== undefined) {
    const text = payload ?? readTextSource(requireString(file, "file"), "file");
    return parseJsonObject(text, "create-job payload") as unknown as UbagCreateJobRequest;
  }

  const prompt = getOption(args, "prompt") ?? "Hello UBAG";
  const input = getOption(args, "input-json") === undefined
    ? ({ prompt } satisfies UbagJsonObject)
    : parseJsonObject(getOption(args, "input-json") ?? "", "input-json");
  const optionsJson = getOption(args, "options-json");
  const job: UbagJobCommand = {
    target: getOption(args, "target") ?? "mock_target",
    command_type: getOption(args, "command-type") ?? "mock.complete",
    input
  };

  if (optionsJson !== undefined) {
    job.options = parseJsonObject(optionsJson, "options-json") as UbagJobOptions;
  }

  const conversation = getOption(args, "conversation");
  if (conversation !== undefined) {
    job.conversation_id = conversation;
  }

  const modelSettings = buildModelSettings(args);
  if (modelSettings !== undefined) {
    (job as UbagJobCommand & { model_settings?: Record<string, string | boolean> }).model_settings = modelSettings;
  }

  const conversationMissing = getOption(args, "conversation-missing");
  if (conversationMissing !== undefined) {
    const options = (job.options ?? {}) as UbagJobOptions;
    options.conversation_missing = conversationMissing;
    job.options = options;
  }

  const request: UbagCreateJobRequest = {
    api_version: config.apiVersion,
    client: {
      app_id: getOption(args, "client-app-id") ?? "ubag-cli",
      app_version: CLI_VERSION
    },
    job
  };

  const idempotencyKey = getOption(args, "idempotency-key");
  if (idempotencyKey !== undefined) {
    request.idempotency_key = idempotencyKey;
  }

  return request;
}

function buildModelSettings(args: ParsedArgs): Record<string, string | boolean> | undefined {
  const settings: Record<string, string | boolean> = {};
  const model = getOption(args, "model");
  const thinking = getOption(args, "thinking");

  if (model !== undefined) {
    settings.model = model;
  }
  if (thinking !== undefined) {
    settings.thinking = thinking;
  }

  return Object.keys(settings).length > 0 ? settings : undefined;
}

function buildListParams(args: ParsedArgs): UbagListJobsParams {
  const params: UbagListJobsParams = {};
  const cursor = getOption(args, "cursor");
  const limit = getOption(args, "limit");
  const status = getOption(args, "status");
  const target = getOption(args, "target");
  const sort = getOption(args, "sort");
  const fields = splitCsv(getOption(args, "fields"));
  const include = splitCsv(getOption(args, "include"));

  if (cursor !== undefined) {
    params.cursor = cursor;
  }
  if (limit !== undefined) {
    params.limit = parsePositiveInt(limit, "limit");
  }
  if (status !== undefined) {
    params.status = status;
  }
  if (target !== undefined) {
    params.target = target;
  }
  if (sort !== undefined) {
    params.sort = sort;
  }
  if (fields.length > 0) {
    params.fields = fields;
  }
  if (include.length > 0) {
    params.include = include;
  }

  return params;
}

function buildListEventsParams(args: ParsedArgs): UbagListEventsParams {
  const params: UbagListEventsParams = {};
  const cursor = getOption(args, "cursor");
  const limit = getOption(args, "limit");
  if (cursor !== undefined) {
    params.cursor = cursor;
  }
  if (limit !== undefined) {
    params.limit = parsePositiveInt(limit, "limit");
  }
  return params;
}

function buildJobEventsParams(args: ParsedArgs): UbagListEventsParams & { after_sequence?: number } {
  const params = buildListEventsParams(args) as UbagListEventsParams & { after_sequence?: number };
  const afterSequence = getOption(args, "after-sequence");
  if (afterSequence !== undefined) {
    params.after_sequence = parseNonNegativeInt(afterSequence, "after-sequence");
  }
  return params;
}

function buildMutationRequest(args: ParsedArgs, config: RuntimeConfig, idempotencyKey: string): UbagJobMutationRequest {
  const payload = getOption(args, "payload");
  const file = getOption(args, "file");

  if (payload !== undefined && file !== undefined) {
    throw new CliUsageError("job mutation accepts either --payload or --file, not both");
  }

  if (payload !== undefined || file !== undefined) {
    const text = payload ?? readTextSource(requireString(file, "file"), "file");
    return parseJsonObject(text, "job mutation payload") as unknown as UbagJobMutationRequest;
  }

  const request: UbagJobMutationRequest = {
    api_version: config.apiVersion,
    idempotency_key: idempotencyKey
  };
  const reason = getOption(args, "reason");
  if (reason !== undefined) {
    request.reason = reason;
  }
  return request;
}

function buildWebhookReplayRequest(args: ParsedArgs, config: RuntimeConfig, idempotencyKey: string): UbagJsonObject {
  const payload = getOption(args, "payload");
  const file = getOption(args, "file");

  if (payload !== undefined && file !== undefined) {
    throw new CliUsageError("webhook replay accepts either --payload or --file, not both");
  }

  if (payload !== undefined || file !== undefined) {
    const text = payload ?? readTextSource(requireString(file, "file"), "file");
    return parseJsonObject(text, "webhook replay payload");
  }

  const request: UbagJsonObject = {
    api_version: config.apiVersion,
    idempotency_key: idempotencyKey,
    reason: getOption(args, "reason") ?? "operator_retry"
  };
  const deliveryId = getOption(args, "delivery-id");
  if (deliveryId !== undefined) {
    request.delivery_id = deliveryId;
  }
  return request;
}

function buildAlertActionRequest(
  args: ParsedArgs,
  config: RuntimeConfig,
  idempotencyKey: string
): UbagAlertActionRequest {
  const payload = getOption(args, "payload");
  const file = getOption(args, "file");

  if (payload !== undefined && file !== undefined) {
    throw new CliUsageError("alert action accepts either --payload or --file, not both");
  }

  if (payload !== undefined || file !== undefined) {
    const text = payload ?? readTextSource(requireString(file, "file"), "file");
    return parseJsonObject(text, "alert action payload") as unknown as UbagAlertActionRequest;
  }

  const request: UbagAlertActionRequest = {
    api_version: config.apiVersion,
    idempotency_key: idempotencyKey
  };
  const reason = getOption(args, "reason");
  if (reason !== undefined) {
    request.reason = reason;
  }
  return request;
}

function buildSsoLogoutRequest(
  args: ParsedArgs,
  config: RuntimeConfig,
  idempotencyKey: string
): UbagSsoLogoutRequest {
  const payload = getOption(args, "payload");
  const file = getOption(args, "file");

  if (payload !== undefined && file !== undefined) {
    throw new CliUsageError("sso-logout accepts either --payload or --file, not both");
  }

  if (payload !== undefined || file !== undefined) {
    const text = payload ?? readTextSource(requireString(file, "file"), "file");
    return parseJsonObject(text, "sso-logout payload") as unknown as UbagSsoLogoutRequest;
  }

  return {
    api_version: config.apiVersion,
    idempotency_key: idempotencyKey
  };
}

function buildAuditExportRequest(
  args: ParsedArgs,
  config: RuntimeConfig,
  idempotencyKey: string
): UbagAuditExportRequest {
  const payload = getOption(args, "payload");
  const file = getOption(args, "file");

  if (payload !== undefined && file !== undefined) {
    throw new CliUsageError("export-audit accepts either --payload or --file, not both");
  }

  if (payload !== undefined || file !== undefined) {
    const text = payload ?? readTextSource(requireString(file, "file"), "file");
    return parseJsonObject(text, "export-audit payload") as unknown as UbagAuditExportRequest;
  }

  const request: UbagAuditExportRequest = {
    api_version: config.apiVersion,
    idempotency_key: idempotencyKey
  };
  const since = getOption(args, "since");
  const until = getOption(args, "until");
  const limit = getOption(args, "limit");
  if (since !== undefined) {
    request.since = since;
  }
  if (until !== undefined) {
    request.until = until;
  }
  if (limit !== undefined) {
    request.limit = parsePositiveInt(limit, "limit");
  }
  const fromSequence = getOption(args, "from-sequence");
  const toSequence = getOption(args, "to-sequence");
  if (fromSequence !== undefined || toSequence !== undefined) {
    const range: UbagAuditExportRange = {};
    if (fromSequence !== undefined) {
      range.from_sequence = parseNonNegativeInt(fromSequence, "from-sequence");
    }
    if (toSequence !== undefined) {
      range.to_sequence = parseNonNegativeInt(toSequence, "to-sequence");
    }
    request.range = range;
  }
  return request;
}

function defaultMockWorkerPayload(args: ParsedArgs, config: RuntimeConfig): UbagJsonObject {
  const prompt = getOption(args, "prompt") ?? "Hello local worker";
  const target = getOption(args, "target") ?? "mock";
  const commandType = getOption(args, "command-type") ?? "mock.complete";

  return {
    api_version: config.apiVersion,
    idempotency_key: getOption(args, "idempotency-key") ?? generateIdempotencyKey(),
    job: {
      target,
      command_type: commandType,
      input: {
        prompt
      },
      options: {
        mock_tokens: ["Mock ", "worker ", "response"],
        mock_result: `Mock worker response: ${prompt}`
      }
    }
  };
}

function parseCli(argv: string[]): ParsedArgs {
  let command: string | undefined;
  const optionTokens: string[] = [];

  for (let index = 0; index < argv.length; index += 1) {
    const token = argv[index];
    if (token === undefined) {
      continue;
    }

    if (command === undefined && token === "--") {
      continue;
    }

    if (command === undefined && token !== "--" && isOptionToken(token)) {
      optionTokens.push(token);
      const option = parseOptionToken(token);
      if (option !== undefined && option.value === undefined && optionRequiresValue(option.name)) {
        index += 1;
        const value = argv[index];
        if (value === undefined || isOptionToken(value)) {
          throw new CliUsageError(`missing value for --${option.name}`);
        }
        optionTokens.push(value);
      }
      continue;
    }

    if (command === undefined && token !== "--") {
      command = token;
      continue;
    }

    optionTokens.push(token);
  }

  const parsed = parseOptions(optionTokens);
  return {
    command,
    options: parsed.options,
    flags: parsed.flags,
    positionals: parsed.positionals
  };
}

function parseOptions(tokens: string[]): Omit<ParsedArgs, "command"> {
  const options = new Map<string, string>();
  const flags = new Set<string>();
  const positionals: string[] = [];
  let positionalOnly = false;

  for (let index = 0; index < tokens.length; index += 1) {
    const token = tokens[index];
    if (token === undefined) {
      continue;
    }

    if (positionalOnly) {
      positionals.push(token);
      continue;
    }

    if (token === "--") {
      positionalOnly = true;
      continue;
    }

    if (!isOptionToken(token)) {
      positionals.push(token);
      continue;
    }

    const option = parseOptionToken(token);
    if (option === undefined) {
      throw new CliUsageError(`invalid option "${token}"`);
    }

    if (BOOLEAN_OPTIONS.has(option.name)) {
      flags.add(option.name);
      continue;
    }

    if (!VALUE_OPTIONS.has(option.name)) {
      throw new CliUsageError(`unknown option "--${option.name}"`);
    }

    const value = option.value ?? tokens[index + 1];
    if (value === undefined || (option.value === undefined && isOptionToken(value))) {
      throw new CliUsageError(`missing value for --${option.name}`);
    }
    if (option.value === undefined) {
      index += 1;
    }
    options.set(option.name, value);
  }

  return {
    options,
    flags,
    positionals
  };
}

function parseOptionToken(token: string): { name: string; value: string | undefined } | undefined {
  if (token.startsWith("--")) {
    const withoutPrefix = token.slice(2);
    const equalsIndex = withoutPrefix.indexOf("=");
    const name = equalsIndex === -1 ? withoutPrefix : withoutPrefix.slice(0, equalsIndex);
    const value = equalsIndex === -1 ? undefined : withoutPrefix.slice(equalsIndex + 1);
    if (name === "") {
      return undefined;
    }
    return {
      name,
      value
    };
  }

  if (token.startsWith("-") && token.length === 2) {
    const alias = OPTION_ALIASES.get(token.slice(1));
    if (alias === undefined) {
      return undefined;
    }
    return {
      name: alias,
      value: undefined
    };
  }

  return undefined;
}

function optionRequiresValue(name: string): boolean {
  return VALUE_OPTIONS.has(name) && !BOOLEAN_OPTIONS.has(name);
}

function isOptionToken(token: string): boolean {
  return token.startsWith("-") && token !== "-";
}

function getOption(args: ParsedArgs, name: string): string | undefined {
  return args.options.get(name);
}

function hasFlag(args: ParsedArgs, name: string): boolean {
  return args.flags.has(name);
}

function requirePositional(args: ParsedArgs, index: number, name: string): string {
  const value = args.positionals[index];
  if (value === undefined || value === "") {
    throw new CliUsageError(`missing ${name}`);
  }
  return value;
}

function requireString(value: string | undefined, name: string): string {
  if (value === undefined || value === "") {
    throw new CliUsageError(`missing ${name}`);
  }
  return value;
}

function readTextSource(source: string, label: string): string {
  if (source === "-") {
    return readFileSync(0, "utf8");
  }

  try {
    return readFileSync(resolve(source), "utf8");
  } catch (error) {
    throw new CliRuntimeError(`could not read ${label} "${source}"`, error);
  }
}

function parseJsonObject(text: string, label: string): UbagJsonObject {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch (error) {
    throw new CliUsageError(`${label} must be valid JSON`, error);
  }

  if (!isJsonObject(value)) {
    throw new CliUsageError(`${label} must be a JSON object`);
  }

  return value as UbagJsonObject;
}

function isJsonObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function parsePositiveInt(value: string, label: string): number {
  const parsed = Number(value);
  if (!Number.isInteger(parsed) || parsed <= 0) {
    throw new CliUsageError(`${label} must be a positive integer`);
  }
  return parsed;
}

function parseNonNegativeInt(value: string, label: string): number {
  const parsed = Number(value);
  if (!Number.isInteger(parsed) || parsed < 0) {
    throw new CliUsageError(`${label} must be a non-negative integer`);
  }
  return parsed;
}

function splitCsv(value: string | undefined): string[] {
  if (value === undefined || value.trim() === "") {
    return [];
  }

  return value
    .split(",")
    .map((entry) => entry.trim())
    .filter((entry) => entry !== "");
}

function normalizeBaseUrl(baseUrl: string): string {
  return baseUrl.replace(/\/+$/, "/");
}

async function readBoundedEventStream(response: Response, maxEvents: number): Promise<string> {
  if (response.body === null) {
    return response.text();
  }

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let output = "";

  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }

      output += decoder.decode(value, { stream: true });
      if (parseSseEvents(output).length >= maxEvents) {
        await reader.cancel();
        break;
      }
    }
  } finally {
    output += decoder.decode();
    reader.releaseLock();
  }

  return output;
}

function parseSseEvents(text: string): SseEvent[] {
  const blocks = text
    .split(/\r?\n\r?\n/)
    .map((block) => block.trim())
    .filter((block) => block !== "");
  const events: SseEvent[] = [];

  for (const block of blocks) {
    const event: SseEvent = {};
    const dataLines: string[] = [];

    for (const line of block.split(/\r?\n/)) {
      if (line.startsWith(":")) {
        continue;
      }

      const colonIndex = line.indexOf(":");
      const field = colonIndex === -1 ? line : line.slice(0, colonIndex);
      const value = colonIndex === -1 ? "" : line.slice(colonIndex + 1).replace(/^ /, "");

      if (field === "event") {
        event.event = value;
      } else if (field === "data") {
        dataLines.push(value);
      } else if (field === "id") {
        event.id = value;
      } else if (field === "retry") {
        event.retry = value;
      }
    }

    if (dataLines.length > 0) {
      event.rawData = dataLines.join("\n");
      event.data = parseJsonMaybe(event.rawData);
    }

    events.push(event);
  }

  return events;
}

function parseJsonMaybe(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

async function buildHttpError(response: Response, url: string, method: string): Promise<CliRuntimeError> {
  const text = await response.text();
  return new CliRuntimeError(`${method} ${url} failed with HTTP ${response.status} ${response.statusText}: ${text}`);
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}

function printJson(value: unknown, args: ParsedArgs): void {
  const compact = hasFlag(args, "json") && !hasFlag(args, "pretty");
  console.log(JSON.stringify(value, null, compact ? 0 : 2));
}

function printHelp(topic: string | undefined): void {
  if (topic === "create-job") {
    console.log(`Usage: ubag create-job [options]

Create a gateway job with either a full payload or convenience fields.

Options:
  --target <target>             Job target. Defaults to mock_target.
  --command-type <type>         Job command type. Defaults to mock.complete.
  --prompt <text>               Builds {"prompt": text} input.
  --input-json <json>           Job input object.
  --options-json <json>         Job options object.
  --model <value>               Sets job.model_settings.model (provider setting key).
  --thinking <value>            Sets job.model_settings.thinking (provider setting key).
  --conversation <key>          Sets job.conversation_id to resume a provider chat thread.
  --conversation-missing <mode> Sets job.options.conversation_missing (fail|restart).
  --attach <paths>              Comma-separated file paths to attach (multipart one-shot).
                                Content-type and kind are inferred from the extension.
  --payload <json>              Full create-job request envelope.
  --file <path|->               Full create-job request envelope from file or stdin.
  --idempotency-key <key>       Idempotency key for this create request.
  --client-app-id <id>          Client metadata app_id. Defaults to ubag-cli.`);
    return;
  }

  if (topic === "mock-run") {
    console.log(`Usage: ubag mock-run [options]

Invoke apps/worker/run_mock_worker.py from the repo root.

Options:
  --payload <json>              Inline worker payload.
  --input, -i <path|->          Worker payload file or stdin.
  --output, -o <path>           JSONL output file.
  --prompt <text>               Prompt for the generated default payload.
  --python <exe>                Python executable. Defaults to UBAG_PYTHON or python.`);
    return;
  }

  if (topic === "adapter-test") {
    console.log(`Usage: ubag adapter-test [options]

Run the safe local adapter smoke test command.

Options:
  --target <target>             Adapter target. Defaults to mock.
  --prompt <text>               Prompt for the generated default payload.
  --python <exe>                Python executable. Defaults to UBAG_PYTHON or python.`);
    return;
  }

  if (topic === "diagnose") {
    console.log(`Usage: ubag diagnose [options]

Print CLI runtime configuration and gateway health diagnostics.`);
    return;
  }

  if (topic === "cancel-job" || topic === "retry-job") {
    console.log(`Usage: ubag ${topic} <job_id> [options]

Idempotently ${topic === "cancel-job" ? "cancel" : "retry"} a gateway job.

Options:
  --idempotency-key <key>       Optional idempotency key. Generated when omitted.
  --reason <text>               Optional operator reason.
  --payload <json>              Full mutation request envelope.
  --file <path|->               Full mutation request envelope from file or stdin.`);
    return;
  }

  if (["list-events", "list-targets", "list-adapters", "list-webhooks", "list-apps", "list-devices", "list-audit-events"].includes(topic ?? "")) {
    console.log(`Usage: ubag ${topic} [options]

List a cursor-paginated gateway collection.

Options:
  --cursor <cursor>             Optional pagination cursor.
  --limit <count>               Optional page size.`);
    return;
  }

  if (topic === "list-job-events") {
    console.log(`Usage: ubag list-job-events <job_id> [options]

List historical events for a job.

Options:
  --cursor <sequence>           Event sequence cursor alias.
  --after-sequence <sequence>   Return events after this sequence.
  --limit <count>               Optional page size.`);
    return;
  }

  if (topic === "list-artifacts") {
    console.log(`Usage: ubag list-artifacts <job_id>

List artifact metadata for a job.`);
    return;
  }

  if (topic === "put-artifact") {
    console.log(`Usage: ubag put-artifact <job_id> <key> --file <path> [options]

Idempotently upload artifact bytes for a job.

Options:
  --file <path>                  Artifact source file.
  --content-type <type>          Defaults to application/octet-stream.
  --idempotency-key <key>        Optional idempotency key. Generated when omitted.`);
    return;
  }

  if (topic === "get-artifact") {
    console.log(`Usage: ubag get-artifact <job_id> <key> [options]

Download artifact bytes. Without --output, body_base64 is printed.

Options:
  --output <path>                Write artifact bytes to this file.`);
    return;
  }

  if (topic === "delete-artifact") {
    console.log(`Usage: ubag delete-artifact <job_id> <key> [options]

Idempotently delete an artifact.

Options:
  --idempotency-key <key>       Optional idempotency key. Generated when omitted.`);
    return;
  }

  if (topic === "replay-webhook") {
    console.log(`Usage: ubag replay-webhook [options]

Idempotently request webhook replay.

Options:
  --delivery-id <id>            Delivery id to replay.
  --idempotency-key <key>       Optional idempotency key. Generated when omitted.
  --reason <text>               Optional operator reason.
  --payload <json>              Full replay request envelope.
  --file <path|->               Full replay request envelope from file or stdin.`);
    return;
  }

  if (topic === "stream-sse" || topic === "stream") {
    console.log(`Usage: ubag ${topic} <job_id> [options]

Read a bounded, safe SSE snapshot from /v1/sse/jobs/{job_id}.

Options:
  --max-events <count>          Events to read before closing. Defaults to 1.
  --timeout-ms <ms>             Abort timeout. Defaults to 10000.
  --raw                         Print raw text/event-stream bytes.`);
    return;
  }

  if (topic === "list-alerts") {
    console.log(`Usage: ubag list-alerts [options]

List operational alerts.

Options:
  --status <status>             Filter by alert status.
  --limit <count>               Optional page size.`);
    return;
  }

  if (topic === "alert-config") {
    console.log(`Usage: ubag alert-config

Show the alert sink configuration (no secrets are returned).`);
    return;
  }

  if (topic === "acknowledge-alert" || topic === "resolve-alert") {
    console.log(`Usage: ubag ${topic} <alert_id> [options]

Idempotently ${topic === "acknowledge-alert" ? "acknowledge" : "resolve"} an alert.

Options:
  --idempotency-key <key>       Optional idempotency key. Generated when omitted.
  --reason <text>               Optional operator reason.
  --payload <json>              Full action request envelope.
  --file <path|->               Full action request envelope from file or stdin.`);
    return;
  }

  if (topic === "list-browser-instances") {
    console.log(`Usage: ubag list-browser-instances [options]

List browser instances in the topology.

Options:
  --state <state>               Filter by instance state.
  --limit <count>               Optional page size.`);
    return;
  }

  if (topic === "list-browser-contexts") {
    console.log(`Usage: ubag list-browser-contexts [options]

List provider browser contexts.

Options:
  --instance-id <id>            Filter by browser instance id.
  --limit <count>               Optional page size.`);
    return;
  }

  if (topic === "list-browser-tabs") {
    console.log(`Usage: ubag list-browser-tabs [options]

List browser tabs.

Options:
  --context-id <id>             Filter by provider context id.
  --state <state>               Filter by tab state.
  --limit <count>               Optional page size.`);
    return;
  }

  if (topic === "browser-summary") {
    console.log(`Usage: ubag browser-summary

Show the browser topology summary.`);
    return;
  }

  if (topic === "list-concurrency") {
    console.log(`Usage: ubag list-concurrency [options]

List concurrency ceilings.

Options:
  --cursor <cursor>             Optional pagination cursor.
  --limit <count>               Optional page size.`);
    return;
  }

  if (topic === "sso-logout") {
    console.log(`Usage: ubag sso-logout [options]

Idempotently revoke the current SSO session.

Options:
  --idempotency-key <key>       Optional idempotency key. Generated when omitted.
  --payload <json>              Full request envelope.
  --file <path|->               Full request envelope from file or stdin.`);
    return;
  }

  if (topic === "export-audit") {
    console.log(`Usage: ubag export-audit [options]

Idempotently export a verifiable audit chain segment.

Options:
  --since <value>               Lower bound filter.
  --until <value>               Upper bound filter.
  --limit <count>               Maximum records.
  --from-sequence <seq>         Range lower bound sequence.
  --to-sequence <seq>           Range upper bound sequence.
  --idempotency-key <key>       Optional idempotency key. Generated when omitted.
  --payload <json>              Full export request envelope.
  --file <path|->               Full export request envelope from file or stdin.`);
    return;
  }

  console.log(`Usage: ubag [global options] <command> [command options]

Commands:
  health                        GET /v1/health.
  ready                         GET /v1/ready.
  version                       GET /v1/version.
  diagnose                      Print CLI and gateway diagnostics.
  create-job                    POST /v1/jobs.
  get-job <job_id>              GET /v1/jobs/{job_id}.
  list-jobs                     GET /v1/jobs.
  list-workflows                GET /v1/workflows.
  list-templates                GET /v1/templates.
  list-job-events <job_id>      GET /v1/jobs/{job_id}/events.
  list-events                   GET /v1/events.
  list-apps                     GET /v1/apps.
  list-devices                  GET /v1/devices.
  list-audit-events             GET /v1/audit.
  list-targets                  GET /v1/targets.
  list-adapters                 GET /v1/adapters.
  list-webhooks                 GET /v1/webhooks.
  list-artifacts <job_id>       GET /v1/jobs/{job_id}/artifacts.
  put-artifact <job_id> <key>   PUT /v1/jobs/{job_id}/artifacts/{key}.
  get-artifact <job_id> <key>   GET /v1/jobs/{job_id}/artifacts/{key}.
  delete-artifact <job_id> <key> DELETE /v1/jobs/{job_id}/artifacts/{key}.
  replay-webhook                POST /v1/webhooks/replay.
  cache-status                  GET /v1/cache.
  metrics                       GET /v1/metrics.
  cancel-job <job_id>           POST /v1/jobs/{job_id}/cancel.
  retry-job <job_id>            POST /v1/jobs/{job_id}/retry.
  list-alerts                   GET /v1/alerts.
  alert-config                  GET /v1/alerts/config.
  acknowledge-alert <alert_id>  POST /v1/alerts/{alert_id}/acknowledge.
  resolve-alert <alert_id>      POST /v1/alerts/{alert_id}/resolve.
  list-browser-instances        GET /v1/browser/instances.
  list-browser-contexts         GET /v1/browser/contexts.
  list-browser-tabs             GET /v1/browser/tabs.
  browser-summary               GET /v1/browser/summary.
  list-concurrency              GET /v1/concurrency.
  sso-logout                    POST /v1/sso/logout.
  export-audit                  POST /v1/audit/export.
  stream <job_id>               Alias for stream-sse.
  stream-sse <job_id>           Safe bounded SSE snapshot reader.
  mock-run                      Invoke apps/worker/run_mock_worker.py.
  adapter-test                  Run the safe local adapter smoke test.

Global options:
  --base-url <url>              Defaults to UBAG_BASE_URL, UBAG_GATEWAY_URL, or http://localhost:8080.
  --api-version <version>       Defaults to UBAG_API_VERSION or the SDK default.
  --app-secret <secret>         Defaults to UBAG_APP_SECRET or dev-secret.
  --tenant-id <id>              Sends Ubag-Tenant-Id.
  --scope-app-id <id>           Sends Ubag-App-Id.
  --no-auth                     Do not send Authorization.
  --json                        Compact JSON output.
  --pretty                      Pretty JSON output.
  --help, -h                    Show help.

Examples:
  ubag health
  ubag create-job --target mock_target --command-type echo --prompt "Hello UBAG"
  ubag get-job job_123
  ubag list-jobs --limit 10
  ubag list-job-events job_123 --limit 10
  ubag list-targets
  ubag stream-sse job_123
  ubag mock-run --prompt "Hello local worker"
  ubag adapter-test --target mock`);
}

class CliUsageError extends Error {
  readonly name = "CliUsageError";

  constructor(message: string, cause?: unknown) {
    super(message, cause === undefined ? undefined : { cause });
  }
}

class CliRuntimeError extends Error {
  readonly name = "CliRuntimeError";

  constructor(message: string, cause?: unknown) {
    super(message, cause === undefined ? undefined : { cause });
  }
}

function formatError(error: unknown): { message: string; details?: unknown } {
  if (error instanceof UbagApiError) {
    return {
      message: error.message,
      details: {
        type: error.name,
        status: error.status,
        code: error.code,
        category: error.category,
        retryable: error.retryable,
        retry_after_ms: error.retryAfterMs,
        trace_id: error.traceId
      }
    };
  }

  if (error instanceof UbagTransportError) {
    return {
      message: error.message,
      details: {
        type: error.name,
        method: error.method,
        url: error.url
      }
    };
  }

  if (error instanceof Error) {
    return {
      message: error.message,
      details: {
        type: error.name
      }
    };
  }

  return {
    message: String(error)
  };
}

main(process.argv.slice(2))
  .then((code) => {
    process.exitCode = code;
  })
  .catch((error: unknown) => {
    const formatted = formatError(error);
    console.error(`ubag: ${formatted.message}`);
    if (formatted.details !== undefined) {
      console.error(JSON.stringify(formatted.details, null, 2));
    }
    if (error instanceof CliUsageError) {
      console.error("Run `ubag --help` for usage.");
      process.exitCode = 2;
      return;
    }
    process.exitCode = 1;
  });
