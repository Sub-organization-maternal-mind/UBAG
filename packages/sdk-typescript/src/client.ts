import { UbagApiError, UbagTransportError, isUbagErrorEnvelope } from "./errors.js";
import { type RetryPolicy, DEFAULT_RETRY_POLICY, computeBackoff, shouldRetry } from "./retry.js";
import { type TelemetryOptions, withSpan } from "./telemetry.js";
import {
  UBAG_DEFAULT_API_VERSION,
  UBAG_SDK_NAME,
  UBAG_SDK_VERSION,
  type UbagAlertConfig,
  type UbagAlertActionRequest,
  type UbagAlertListResponse,
  type UbagAlertMutationResponse,
  type UbagAuditExportRequest,
  type UbagAuditExportResult,
  type UbagBrowserInstanceListResponse,
  type UbagBrowserTabListResponse,
  type UbagBrowserTopologySummary,
  type UbagCacheStatusResponse,
  type UbagCollectionResponse,
  type UbagConcurrencyListResponse,
  type UbagConversationListResponse,
  type UbagCreateJobRequest,
  type UbagArtifactDownloadResponse,
  type UbagArtifactListResponse,
  type UbagArtifactResponse,
  type UbagAttachmentUpload,
  type UbagJsonValue,
  type UbagHealthResponse,
  type UbagJobEventsResponse,
  type UbagJobMutationRequest,
  type UbagJobResponse,
  type UbagJsonObject,
  type UbagListAlertsParams,
  type UbagListBrowserInstancesParams,
  type UbagListBrowserTabsParams,
  type UbagListConcurrencyParams,
  type UbagListConversationsParams,
  type UbagListEventsParams,
  type UbagListJobEventsParams,
  type UbagListJobsParams,
  type UbagListJobsResponse,
  type UbagListProviderContextsParams,
  type UbagLogoutResult,
  type UbagProviderContextListResponse,
  type UbagReadyResponse,
  type UbagSsoLogoutRequest,
  type UbagVersionResponse,
  type UbagWebhookReplayRequest,
  type UbagWebhookReplayResponse
} from "./types.js";

export type UbagFetch = (input: string | URL, init?: RequestInit) => Promise<Response>;

export interface UbagClientOptions {
  baseUrl: string | URL;
  appSecret?: string;
  apiVersion?: string;
  fetch?: UbagFetch;
  headers?: Record<string, string>;
  retry?: RetryPolicy;
  telemetry?: TelemetryOptions;
  sidecarDiscovery?: boolean;
}

export interface UbagRequestOptions {
  apiVersion?: string;
  idempotencyKey?: string;
  signal?: AbortSignal;
  headers?: Record<string, string>;
}

interface InternalRequestOptions extends UbagRequestOptions {
  body?: unknown;
}

interface RawRequestOptions extends UbagRequestOptions {
  body?: BodyInit;
  contentType?: string;
}

const JSON_CONTENT_TYPE = "application/json";

export class UbagClient {
  readonly baseUrl: string;
  readonly apiVersion: string;

  private readonly appSecret: string | undefined;
  private readonly fetchImpl: UbagFetch;
  private readonly defaultHeaders: Record<string, string>;
  private readonly retry: RetryPolicy;
  private readonly telemetry: TelemetryOptions | undefined;

  constructor(options: UbagClientOptions) {
    const fetchImpl = options.fetch ?? globalThis.fetch;
    if (typeof fetchImpl !== "function") {
      throw new TypeError("A fetch implementation is required in this runtime.");
    }

    this.baseUrl = normalizeBaseUrl(options.baseUrl);
    this.apiVersion = options.apiVersion ?? UBAG_DEFAULT_API_VERSION;
    this.appSecret = options.appSecret;
    this.fetchImpl = fetchImpl.bind(globalThis) as UbagFetch;
    this.defaultHeaders = { ...options.headers };
    this.retry = options.retry ?? DEFAULT_RETRY_POLICY;
    this.telemetry = options.telemetry;
  }

  async health(options: UbagRequestOptions = {}): Promise<UbagHealthResponse> {
    return this.request("GET", "/v1/health", options);
  }

  async ready(options: UbagRequestOptions = {}): Promise<UbagReadyResponse> {
    return this.request("GET", "/v1/ready", options);
  }

  async version(options: Omit<UbagRequestOptions, "apiVersion" | "idempotencyKey"> = {}): Promise<UbagVersionResponse> {
    return this.request("GET", "/v1/version", options);
  }

  async createJob(request: UbagCreateJobRequest, options: UbagRequestOptions = {}): Promise<UbagJobResponse> {
    const apiVersion = request.api_version ?? options.apiVersion ?? this.apiVersion;
    const idempotencyKey = request.idempotency_key ?? options.idempotencyKey ?? generateIdempotencyKey();
    return this.withRetries("createJob", { "ubag.target": String(request.job?.target ?? "") }, () => {
      const body: UbagCreateJobRequest = {
        ...request,
        api_version: apiVersion,
        idempotency_key: idempotencyKey,
        client: {
          ...request.client,
          sdk: request.client.sdk ?? {
            name: UBAG_SDK_NAME,
            version: UBAG_SDK_VERSION
          }
        }
      };
      return this.request("POST", "/v1/jobs", {
        ...options,
        apiVersion,
        idempotencyKey,
        body
      });
    });
  }

  /**
   * Create a job with attachments via the key-reference flow: submit the job
   * (which is held until its files arrive), then upload every attachment's bytes
   * to the artifact store in parallel. The returned response is the held job; the
   * gateway dispatches it automatically once the final upload lands.
   */
  async submitJobWithAttachments(
    request: UbagCreateJobRequest,
    attachments: UbagAttachmentUpload[],
    options: UbagRequestOptions = {}
  ): Promise<UbagJobResponse> {
    const manifest = attachments.map(({ body, ...meta }) => meta);
    const jobRequest: UbagCreateJobRequest = {
      ...request,
      job: {
        ...request.job,
        input: { ...request.job.input, attachments: manifest as unknown as UbagJsonValue }
      }
    };
    const created = await this.createJob(jobRequest, options);
    await Promise.all(
      attachments.map((attachment) =>
        this.putJobArtifact(created.job_id, attachment.key, attachment.body, {
          contentType: attachment.content_type
        })
      )
    );
    return created;
  }

  async createJobWithAttachments(
    request: UbagCreateJobRequest,
    attachments: UbagAttachmentUpload[],
    options: UbagRequestOptions = {}
  ): Promise<UbagJobResponse> {
    return this.submitJobWithAttachments(request, attachments, options);
  }

  /**
   * Create a job with attachments in a single multipart/form-data request: the
   * job envelope is the first part, followed by one binary file part per
   * attachment (part name === attachment key). The job is born complete and
   * dispatches immediately.
   */
  async createJobMultipart(
    request: UbagCreateJobRequest,
    attachments: UbagAttachmentUpload[],
    options: UbagRequestOptions = {}
  ): Promise<UbagJobResponse> {
    const apiVersion = request.api_version ?? options.apiVersion ?? this.apiVersion;
    const idempotencyKey = request.idempotency_key ?? options.idempotencyKey ?? generateIdempotencyKey();
    const manifest = attachments.map(({ body, ...meta }) => meta);
    const envelope: UbagCreateJobRequest = {
      ...request,
      api_version: apiVersion,
      idempotency_key: idempotencyKey,
      client: {
        ...request.client,
        sdk: request.client.sdk ?? { name: UBAG_SDK_NAME, version: UBAG_SDK_VERSION }
      },
      job: {
        ...request.job,
        input: { ...request.job.input, attachments: manifest as unknown as UbagJsonValue }
      }
    };
    const form = new FormData();
    form.append("job", new Blob([JSON.stringify(envelope)], { type: JSON_CONTENT_TYPE }));
    for (const attachment of attachments) {
      form.append(
        attachment.key,
        new Blob([attachment.body], { type: attachment.content_type }),
        attachment.filename ?? attachment.key
      );
    }
    return this.requestRaw("POST", "/v1/jobs", { ...options, apiVersion, idempotencyKey, body: form });
  }

  async getJob(jobId: string, options: UbagRequestOptions = {}): Promise<UbagJobResponse> {
    return this.request("GET", `/v1/jobs/${encodeURIComponent(jobId)}`, options);
  }

  async listJobs(params: UbagListJobsParams = {}, options: UbagRequestOptions = {}): Promise<UbagListJobsResponse> {
    const query = new URLSearchParams();
    addOptionalQuery(query, "cursor", params.cursor);
    addOptionalQuery(query, "limit", params.limit);
    addOptionalQuery(query, "filter[status]", params.status);
    addOptionalQuery(query, "filter[target]", params.target);
    addOptionalQuery(query, "sort", params.sort);
    addOptionalQuery(query, "fields", params.fields?.join(","));
    addOptionalQuery(query, "include", params.include?.join(","));

    const suffix = query.size > 0 ? `?${query.toString()}` : "";
    return this.request("GET", `/v1/jobs${suffix}`, options);
  }

  async listWorkflows(options: UbagRequestOptions = {}): Promise<UbagCollectionResponse> {
    return this.request("GET", "/v1/workflows", options);
  }

  async listTemplates(options: UbagRequestOptions = {}): Promise<UbagCollectionResponse> {
    return this.request("GET", "/v1/templates", options);
  }

  async listTargets(params: UbagListEventsParams = {}, options: UbagRequestOptions = {}): Promise<UbagCollectionResponse> {
    return this.request("GET", `/v1/targets${buildListQuery(params)}`, options);
  }

  async listAdapters(params: UbagListEventsParams = {}, options: UbagRequestOptions = {}): Promise<UbagCollectionResponse> {
    return this.request("GET", `/v1/adapters${buildListQuery(params)}`, options);
  }

  async listApps(params: UbagListEventsParams = {}, options: UbagRequestOptions = {}): Promise<UbagCollectionResponse> {
    return this.request("GET", `/v1/apps${buildListQuery(params)}`, options);
  }

  async listDevices(params: UbagListEventsParams = {}, options: UbagRequestOptions = {}): Promise<UbagCollectionResponse> {
    return this.request("GET", `/v1/devices${buildListQuery(params)}`, options);
  }

  async listAuditEvents(params: UbagListEventsParams = {}, options: UbagRequestOptions = {}): Promise<UbagCollectionResponse> {
    return this.request("GET", `/v1/audit${buildListQuery(params)}`, options);
  }

  async listWebhooks(params: UbagListEventsParams = {}, options: UbagRequestOptions = {}): Promise<UbagCollectionResponse> {
    return this.request("GET", `/v1/webhooks${buildListQuery(params)}`, options);
  }

  async listEvents(params: UbagListEventsParams = {}, options: UbagRequestOptions = {}): Promise<UbagCollectionResponse> {
    return this.request("GET", `/v1/events${buildListQuery(params)}`, options);
  }

  async listJobEvents(jobId: string, params: UbagListJobEventsParams = {}, options: UbagRequestOptions = {}): Promise<UbagJobEventsResponse> {
    const query = new URLSearchParams();
    addOptionalQuery(query, "cursor", params.cursor);
    addOptionalQuery(query, "after_sequence", params.after_sequence);
    addOptionalQuery(query, "limit", params.limit);
    const suffix = query.size > 0 ? `?${query.toString()}` : "";
    return this.request("GET", `/v1/jobs/${encodeURIComponent(jobId)}/events${suffix}`, options);
  }

  async listJobArtifacts(jobId: string, options: UbagRequestOptions = {}): Promise<UbagArtifactListResponse> {
    return this.request("GET", `/v1/jobs/${encodeURIComponent(jobId)}/artifacts`, options);
  }

  async putJobArtifact(jobId: string, key: string, body: BodyInit, options: UbagRequestOptions & { contentType?: string } = {}): Promise<UbagArtifactResponse> {
    const idempotencyKey = options.idempotencyKey ?? generateIdempotencyKey();
    return this.requestRaw("PUT", `/v1/jobs/${encodeURIComponent(jobId)}/artifacts/${encodeURIComponent(key)}`, {
      ...options,
      idempotencyKey,
      body,
      contentType: options.contentType ?? "application/octet-stream"
    });
  }

  async getJobArtifact(jobId: string, key: string, options: UbagRequestOptions = {}): Promise<UbagArtifactDownloadResponse> {
    const response = await this.fetchRaw("GET", `/v1/jobs/${encodeURIComponent(jobId)}/artifacts/${encodeURIComponent(key)}`, options);
    const artifact: UbagArtifactDownloadResponse = {
      body: new Uint8Array(await response.arrayBuffer())
    };
    const contentType = response.headers.get("content-type");
    const checksum = response.headers.get("ubag-artifact-checksum");
    if (contentType !== null) artifact.content_type = contentType;
    if (checksum !== null) artifact.checksum = checksum;
    return artifact;
  }

  async deleteJobArtifact(jobId: string, key: string, options: UbagRequestOptions = {}): Promise<void> {
    const idempotencyKey = options.idempotencyKey ?? generateIdempotencyKey();
    await this.requestRaw<void>("DELETE", `/v1/jobs/${encodeURIComponent(jobId)}/artifacts/${encodeURIComponent(key)}`, {
      ...options,
      idempotencyKey
    });
  }

  async replayWebhookDelivery(request: UbagWebhookReplayRequest = {}, options: UbagRequestOptions = {}): Promise<UbagWebhookReplayResponse> {
    const apiVersion = request.api_version ?? options.apiVersion ?? this.apiVersion;
    const idempotencyKey = request.idempotency_key ?? options.idempotencyKey ?? generateIdempotencyKey();
    return this.request("POST", "/v1/webhooks/replay", {
      ...options,
      apiVersion,
      idempotencyKey,
      body: {
        ...request,
        api_version: apiVersion,
        idempotency_key: idempotencyKey
      }
    });
  }

  async cacheStatus(options: UbagRequestOptions = {}): Promise<UbagCacheStatusResponse> {
    return this.request("GET", "/v1/cache", options);
  }

  async listConversations(params: UbagListConversationsParams = {}, options: UbagRequestOptions = {}): Promise<UbagConversationListResponse> {
    const query = new URLSearchParams();
    addOptionalQuery(query, "limit", params.limit);
    const suffix = query.size > 0 ? `?${query.toString()}` : "";
    return this.request("GET", `/v1/conversations${suffix}`, options);
  }

  async listAlerts(params: UbagListAlertsParams = {}, options: UbagRequestOptions = {}): Promise<UbagAlertListResponse> {
    const query = new URLSearchParams();
    addOptionalQuery(query, "limit", params.limit);
    addOptionalQuery(query, "status", params.status);
    const suffix = query.size > 0 ? `?${query.toString()}` : "";
    return this.request("GET", `/v1/alerts${suffix}`, options);
  }

  async getAlertConfig(options: UbagRequestOptions = {}): Promise<UbagAlertConfig> {
    return this.request("GET", "/v1/alerts/config", options);
  }

  async acknowledgeAlert(
    alertId: string,
    request: UbagAlertActionRequest = {},
    options: UbagRequestOptions = {}
  ): Promise<UbagAlertMutationResponse> {
    const apiVersion = request.api_version ?? options.apiVersion ?? this.apiVersion;
    const idempotencyKey = request.idempotency_key ?? options.idempotencyKey ?? generateIdempotencyKey();
    return this.request("POST", `/v1/alerts/${encodeURIComponent(alertId)}/acknowledge`, {
      ...options,
      apiVersion,
      idempotencyKey,
      body: {
        ...request,
        api_version: apiVersion,
        idempotency_key: idempotencyKey
      }
    });
  }

  async resolveAlert(
    alertId: string,
    request: UbagAlertActionRequest = {},
    options: UbagRequestOptions = {}
  ): Promise<UbagAlertMutationResponse> {
    const apiVersion = request.api_version ?? options.apiVersion ?? this.apiVersion;
    const idempotencyKey = request.idempotency_key ?? options.idempotencyKey ?? generateIdempotencyKey();
    return this.request("POST", `/v1/alerts/${encodeURIComponent(alertId)}/resolve`, {
      ...options,
      apiVersion,
      idempotencyKey,
      body: {
        ...request,
        api_version: apiVersion,
        idempotency_key: idempotencyKey
      }
    });
  }

  async listBrowserInstances(
    params: UbagListBrowserInstancesParams = {},
    options: UbagRequestOptions = {}
  ): Promise<UbagBrowserInstanceListResponse> {
    const query = new URLSearchParams();
    addOptionalQuery(query, "limit", params.limit);
    addOptionalQuery(query, "state", params.state);
    const suffix = query.size > 0 ? `?${query.toString()}` : "";
    return this.request("GET", `/v1/browser/instances${suffix}`, options);
  }

  async listProviderContexts(
    params: UbagListProviderContextsParams = {},
    options: UbagRequestOptions = {}
  ): Promise<UbagProviderContextListResponse> {
    const query = new URLSearchParams();
    addOptionalQuery(query, "limit", params.limit);
    addOptionalQuery(query, "instance_id", params.instance_id);
    const suffix = query.size > 0 ? `?${query.toString()}` : "";
    return this.request("GET", `/v1/browser/contexts${suffix}`, options);
  }

  async listBrowserTabs(
    params: UbagListBrowserTabsParams = {},
    options: UbagRequestOptions = {}
  ): Promise<UbagBrowserTabListResponse> {
    const query = new URLSearchParams();
    addOptionalQuery(query, "limit", params.limit);
    addOptionalQuery(query, "context_id", params.context_id);
    addOptionalQuery(query, "state", params.state);
    const suffix = query.size > 0 ? `?${query.toString()}` : "";
    return this.request("GET", `/v1/browser/tabs${suffix}`, options);
  }

  async getBrowserTopologySummary(options: UbagRequestOptions = {}): Promise<UbagBrowserTopologySummary> {
    return this.request("GET", "/v1/browser/summary", options);
  }

  async getConcurrency(
    params: UbagListConcurrencyParams = {},
    options: UbagRequestOptions = {}
  ): Promise<UbagConcurrencyListResponse> {
    const query = new URLSearchParams();
    addOptionalQuery(query, "cursor", params.cursor);
    addOptionalQuery(query, "limit", params.limit);
    const suffix = query.size > 0 ? `?${query.toString()}` : "";
    return this.request("GET", `/v1/concurrency${suffix}`, options);
  }

  async ssoLogout(request: UbagSsoLogoutRequest = {}, options: UbagRequestOptions = {}): Promise<UbagLogoutResult> {
    const apiVersion = request.api_version ?? options.apiVersion ?? this.apiVersion;
    const idempotencyKey = request.idempotency_key ?? options.idempotencyKey ?? generateIdempotencyKey();
    return this.request("POST", "/v1/sso/logout", {
      ...options,
      apiVersion,
      idempotencyKey,
      body: {
        ...request,
        api_version: apiVersion,
        idempotency_key: idempotencyKey
      }
    });
  }

  async exportAudit(request: UbagAuditExportRequest = {}, options: UbagRequestOptions = {}): Promise<UbagAuditExportResult> {
    const apiVersion = request.api_version ?? options.apiVersion ?? this.apiVersion;
    const idempotencyKey = request.idempotency_key ?? options.idempotencyKey ?? generateIdempotencyKey();
    return this.request("POST", "/v1/audit/export", {
      ...options,
      apiVersion,
      idempotencyKey,
      body: {
        ...request,
        api_version: apiVersion,
        idempotency_key: idempotencyKey
      }
    });
  }

  async getMetrics(options: Omit<UbagRequestOptions, "idempotencyKey"> = {}): Promise<string> {
    const response = await this.fetchRaw("GET", "/v1/metrics", {
      ...options,
      headers: {
        Accept: "text/plain",
        ...options.headers
      }
    });
    return response.text();
  }

  async streamJobEventsSse(jobId: string, options: Omit<UbagRequestOptions, "idempotencyKey"> = {}): Promise<Response> {
    return this.fetchRaw("GET", `/v1/sse/jobs/${encodeURIComponent(jobId)}`, {
      ...options,
      headers: {
        Accept: "text/event-stream",
        ...options.headers
      }
    });
  }

  async streamEventsWebSocket(options: Omit<UbagRequestOptions, "idempotencyKey"> = {}): Promise<UbagJsonObject> {
    return this.request("GET", "/v1/stream", {
      ...options,
      headers: {
        Upgrade: "websocket",
        ...options.headers
      }
    });
  }

  async cancelJob(jobId: string, request: UbagJobMutationRequest = {}, options: UbagRequestOptions = {}): Promise<UbagJobResponse> {
    return this.mutateJob(jobId, "cancel", request, options);
  }

  async retryJob(jobId: string, request: UbagJobMutationRequest = {}, options: UbagRequestOptions = {}): Promise<UbagJobResponse> {
    return this.mutateJob(jobId, "retry", request, options);
  }

  private async mutateJob(
    jobId: string,
    operation: "cancel" | "retry",
    request: UbagJobMutationRequest,
    options: UbagRequestOptions
  ): Promise<UbagJobResponse> {
    const apiVersion = request.api_version ?? options.apiVersion ?? this.apiVersion;
    const idempotencyKey = request.idempotency_key ?? options.idempotencyKey ?? generateIdempotencyKey();
    const body: UbagJobMutationRequest = {
      ...request,
      api_version: apiVersion,
      idempotency_key: idempotencyKey
    };

    return this.request("POST", `/v1/jobs/${encodeURIComponent(jobId)}/${operation}`, {
      ...options,
      apiVersion,
      idempotencyKey,
      body
    });
  }

  private async withRetries<T>(
    op: string,
    attrs: Record<string, string>,
    fn: () => Promise<T>,
  ): Promise<T> {
    return withSpan(this.telemetry, `ubag.${op}`, attrs, async () => {
      let attempt = 0;
      for (;;) {
        try {
          return await fn();
        } catch (err) {
          const isRetryable = err instanceof UbagApiError && err.retryable;
          if (!isRetryable || !shouldRetry(this.retry, attempt, true)) throw err;
          const apiErr = err as UbagApiError;
          const delay = apiErr.retryAfterMs ?? computeBackoff(this.retry, attempt);
          await new Promise<void>((r) => setTimeout(r, delay));
          attempt++;
        }
      }
    });
  }

  private async request<T>(method: string, path: string, options: InternalRequestOptions = {}): Promise<T> {
    const url = new URL(path, this.baseUrl);
    const headers = new Headers({
      Accept: JSON_CONTENT_TYPE,
      "Ubag-Api-Version": options.apiVersion ?? this.apiVersion,
      "Ubag-Sdk-Name": UBAG_SDK_NAME,
      "Ubag-Sdk-Version": UBAG_SDK_VERSION,
      ...this.defaultHeaders,
      ...options.headers
    });

    if (this.appSecret !== undefined && !headers.has("Authorization")) {
      headers.set("Authorization", `Bearer ${this.appSecret}`);
    }

    if (options.idempotencyKey !== undefined) {
      headers.set("Idempotency-Key", options.idempotencyKey);
    }

    const init: RequestInit = {
      method,
      headers
    };

    if (options.signal !== undefined) {
      init.signal = options.signal;
    }

    if (options.body !== undefined) {
      headers.set("Content-Type", JSON_CONTENT_TYPE);
      init.body = JSON.stringify(options.body);
    }

    let response: Response;
    try {
      response = await this.fetchImpl(url, init);
    } catch (cause) {
      throw new UbagTransportError("UBAG API request could not be sent.", {
        url: url.toString(),
        method,
        cause
      });
    }

    if (!response.ok) {
      throw await buildApiError(response, url.toString(), method);
    }

    if (response.status === 204) {
      return undefined as T;
    }

    return (await response.json()) as T;
  }

  private async requestRaw<T>(method: string, path: string, options: RawRequestOptions = {}): Promise<T> {
    const response = await this.fetchRaw(method, path, options);
    if (response.status === 204) {
      return undefined as T;
    }
    return (await response.json()) as T;
  }

  private async fetchRaw(method: string, path: string, options: RawRequestOptions = {}): Promise<Response> {
    const url = new URL(path, this.baseUrl);
    const headers = new Headers({
      Accept: JSON_CONTENT_TYPE,
      "Ubag-Api-Version": options.apiVersion ?? this.apiVersion,
      "Ubag-Sdk-Name": UBAG_SDK_NAME,
      "Ubag-Sdk-Version": UBAG_SDK_VERSION,
      ...this.defaultHeaders,
      ...options.headers
    });

    if (this.appSecret !== undefined && !headers.has("Authorization")) {
      headers.set("Authorization", `Bearer ${this.appSecret}`);
    }
    if (options.idempotencyKey !== undefined) {
      headers.set("Idempotency-Key", options.idempotencyKey);
    }

    const init: RequestInit = { method, headers };
    if (options.signal !== undefined) {
      init.signal = options.signal;
    }
    if (options.body !== undefined) {
      if (options.contentType !== undefined) {
        headers.set("Content-Type", options.contentType);
      } else if (!(typeof FormData !== "undefined" && options.body instanceof FormData)) {
        // FormData must keep the multipart boundary the fetch impl sets; only
        // default a content type for non-FormData raw bodies.
        headers.set("Content-Type", "application/octet-stream");
      }
      init.body = options.body;
    }

    let response: Response;
    try {
      response = await this.fetchImpl(url, init);
    } catch (cause) {
      throw new UbagTransportError("UBAG API request could not be sent.", {
        url: url.toString(),
        method,
        cause
      });
    }
    if (!response.ok) {
      throw await buildApiError(response, url.toString(), method);
    }
    return response;
  }
}

export function createUbagClient(options: UbagClientOptions): UbagClient {
  return new UbagClient(options);
}

export function generateIdempotencyKey(now = Date.now()): string {
  return encodeBase32(BigInt(now), 10) + encodeRandomBase32(10);
}

async function buildApiError(response: Response, url: string, method: string): Promise<UbagApiError> {
  const text = await response.text();
  const body = parseJson(text);
  const envelope = isUbagErrorEnvelope(body) ? body : undefined;

  return new UbagApiError({
    status: response.status,
    statusText: response.statusText,
    url,
    method,
    headers: headersToRecord(response.headers),
    envelope,
    body: body ?? text
  });
}

function normalizeBaseUrl(baseUrl: string | URL): string {
  const url = new URL(baseUrl);
  return url.toString().replace(/\/+$/, "/");
}

function addOptionalQuery(query: URLSearchParams, key: string, value: string | number | undefined): void {
  if (value !== undefined && value !== "") {
    query.set(key, String(value));
  }
}

function buildListQuery(params: UbagListEventsParams): string {
  const query = new URLSearchParams();
  addOptionalQuery(query, "cursor", params.cursor);
  addOptionalQuery(query, "limit", params.limit);
  return query.size > 0 ? `?${query.toString()}` : "";
}

function parseJson(text: string): unknown {
  if (text.trim() === "") {
    return undefined;
  }

  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

function headersToRecord(headers: Headers): Record<string, string> {
  const record: Record<string, string> = {};
  for (const [key, value] of headers.entries()) {
    record[key.toLowerCase()] = value;
  }
  return record;
}

const CROCKFORD_BASE32 = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";

function encodeBase32(value: bigint, length: number): string {
  let output = "";
  let remaining = value;
  for (let index = 0; index < length; index += 1) {
    output = CROCKFORD_BASE32[Number(remaining % 32n)] + output;
    remaining = remaining / 32n;
  }
  return output;
}

function encodeRandomBase32(byteLength: number): string {
  const bytes = new Uint8Array(byteLength);
  const crypto = globalThis.crypto;

  if (crypto?.getRandomValues !== undefined) {
    crypto.getRandomValues(bytes);
  } else {
    for (let index = 0; index < bytes.length; index += 1) {
      bytes[index] = Math.floor(Math.random() * 256);
    }
  }

  let output = "";
  let buffer = 0;
  let bits = 0;

  for (const byte of bytes) {
    buffer = (buffer << 8) | byte;
    bits += 8;

    while (bits >= 5) {
      output += CROCKFORD_BASE32[(buffer >> (bits - 5)) & 31];
      bits -= 5;
    }
  }

  return output;
}
