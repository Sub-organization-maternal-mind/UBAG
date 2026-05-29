<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { settings } from "../lib/settings";
  import {
    getHealth,
    getReadiness,
    getVersion,
    getMetricsText,
    parseMetrics,
    sumMetric,
    ApiError,
  } from "../lib/api";
  import type { HealthStatus, ReadinessStatus, VersionStatus } from "../lib/types";
  import { formatNumber, relativeTime } from "../lib/format";
  import AsyncState from "./AsyncState.svelte";

  let health: HealthStatus | null = null;
  let ready: ReadinessStatus | null = null;
  let version: VersionStatus | null = null;
  let metrics: { label: string; value: number | null; tone: string }[] = [];

  let loading = true;
  let error: string | null = null;
  let lastSync: string | null = null;
  let timer: ReturnType<typeof setInterval> | undefined;

  async function load() {
    error = null;
    const gw = $settings.gatewayUrl;
    try {
      const results = await Promise.allSettled([
        getHealth(gw),
        getReadiness(gw),
        getVersion(gw),
        getMetricsText(gw),
      ]);

      health = results[0].status === "fulfilled" ? results[0].value : null;
      ready = results[1].status === "fulfilled" ? results[1].value : null;
      version = results[2].status === "fulfilled" ? results[2].value : null;

      if (results[3].status === "fulfilled") {
        const samples = parseMetrics(results[3].value);
        metrics = [
          {
            label: "Jobs processed",
            value: sumMetric(samples, "ubag_worker_jobs_processed_total"),
            tone: "var(--tone-ready)",
          },
          {
            label: "HTTP requests",
            value: sumMetric(samples, "ubag_gateway_http_requests_total"),
            tone: "var(--tone-running)",
          },
          {
            label: "SSE connections",
            value: sumMetric(samples, "ubag_gateway_sse_connections"),
            tone: "var(--tone-running)",
          },
          {
            label: "Queue depth",
            value: sumMetric(samples, "ubag_gateway_queue_depth"),
            tone: "var(--tone-warn)",
          },
        ].filter((m) => m.value !== null);
      } else {
        metrics = [];
      }

      // Treat a total failure (all rejected) as an error worth surfacing.
      if (!health && !ready && !version) {
        const first = results.find((r) => r.status === "rejected") as
          | PromiseRejectedResult
          | undefined;
        const reason = first?.reason;
        error =
          reason instanceof ApiError
            ? reason.message
            : "Gateway is unreachable. Check the connection settings.";
      }
      lastSync = new Date().toISOString();
    } catch (err) {
      error = err instanceof Error ? err.message : "Failed to load overview";
    } finally {
      loading = false;
    }
  }

  function startTimer() {
    stopTimer();
    timer = setInterval(load, Math.max(5, $settings.refreshSeconds) * 1000);
  }
  function stopTimer() {
    if (timer) {
      clearInterval(timer);
      timer = undefined;
    }
  }

  onMount(() => {
    load();
    startTimer();
  });
  onDestroy(stopTimer);

  // Restart the poll loop when the refresh interval changes.
  $: if (timer) {
    void $settings.refreshSeconds;
    startTimer();
  }
</script>

<section aria-labelledby="overview-title">
  <div class="row-between" style="margin-bottom: var(--space-3)">
    <div>
      <p class="section-kicker">Operations</p>
      <h2 id="overview-title" class="view-title" style="margin-bottom:0">Overview</h2>
    </div>
    <button class="icon-btn" aria-label="Refresh" on:click={load}>↻</button>
  </div>

  {#if loading && !health && !ready}
    <AsyncState loading={true} />
  {:else if error}
    <AsyncState error={error} onRetry={load} />
  {:else}
    <div class="card">
      <div class="row-between">
        <h3>Service health</h3>
        <span
          class="badge"
          data-tone={health?.status === "ok" ? "ready" : "danger"}
        >
          <span class="dot" data-tone={health?.status === "ok" ? "ready" : "danger"} aria-hidden="true"></span>
          {health?.status === "ok" ? "Healthy" : "Down"}
        </span>
      </div>
      <div class="row-between" style="margin-top: var(--space-2)">
        <span class="muted">Readiness</span>
        <span class="badge" data-tone={ready?.ready ? "ready" : "warn"}>
          {ready?.ready ? "Ready" : ready ? "Not ready" : "Unknown"}
        </span>
      </div>
      {#if version}
        <div class="row-between" style="margin-top: var(--space-2)">
          <span class="muted">Version</span>
          <span class="mono">{version.service} · {version.version}</span>
        </div>
      {/if}
      {#if lastSync}
        <p class="hint" style="margin-top: var(--space-2)">Last sync {relativeTime(lastSync)}</p>
      {/if}
    </div>

    {#if ready?.dependencies && ready.dependencies.length > 0}
      <div class="card">
        <h3>Dependencies</h3>
        <div class="list" style="margin-top: var(--space-2)">
          {#each ready.dependencies as dep}
            <div class="list-item">
              <div>
                <div class="primary">{dep.name}</div>
                {#if dep.message}<div class="secondary">{dep.message}</div>{/if}
              </div>
              <span
                class="badge"
                data-tone={dep.status === "ready" ? "ready" : dep.status === "degraded" ? "warn" : "danger"}
              >
                {dep.status}{#if dep.latency_ms != null} · {dep.latency_ms}ms{/if}
              </span>
            </div>
          {/each}
        </div>
      </div>
    {/if}

    {#if metrics.length > 0}
      <p class="section-kicker">Key metrics</p>
      <div class="metric-grid">
        {#each metrics as m}
          <div class="metric-card" style="--tone: {m.tone}">
            <span class="label">{m.label}</span>
            <span class="value">{formatNumber(m.value)}</span>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</section>
