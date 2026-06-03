<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import { settings } from '$lib/stores/settings';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import type { MetricsResponse } from '$lib/api/types';
  import Chart from 'chart.js/auto';

  let metrics = $state<MetricsResponse | null>(null);
  let loading = $state(true);
  let denied = $state(false);
  let error = $state<string | null>(null);

  let chartCanvas = $state<HTMLCanvasElement | undefined>(undefined);
  let chart: Chart | undefined;

  // Top 5 numeric metrics for bar chart
  let chartLabels = $state<string[]>([]);
  let chartValues = $state<number[]>([]);

  // Grafana URL derived from gateway URL (replace port 8081 with 3000)
  let grafanaUrl = $derived(
    $settings.gatewayUrl.replace(/:\d+$/, ':3000') + '/d/ubag-main'
  );
  let grafanaVisible = $state(false);
  let grafanaError = $state(false);

  function buildChart() {
    if (!chartCanvas || chartLabels.length === 0) return;
    chart?.destroy();
    chart = new Chart(chartCanvas, {
      type: 'bar',
      data: {
        labels: chartLabels,
        datasets: [{
          label: 'Value',
          data: chartValues,
          backgroundColor: 'oklch(58% 0.18 35 / 0.7)',
          borderRadius: 4,
        }],
      },
      options: {
        responsive: true,
        plugins: { legend: { display: false } },
        scales: {
          y: { beginAtZero: true },
        },
      },
    });
  }

  const FAILED_STATES = new Set(['failed', 'error', 'dead', 'dlq']);

  async function load() {
    // The Prometheus /v1/metrics endpoint is edge-blocked and is not JSON, so
    // aggregate operator metrics from the resource endpoints instead.
    loading = true;
    error = null;
    denied = false;

    const [jobsRes, targetsRes, adaptersRes, browserRes] = await Promise.all([
      api.get<{ jobs?: Array<{ status?: string }>; total?: number }>('/v1/jobs?limit=200'),
      api.get('/v1/targets'),
      api.get('/v1/adapters'),
      api.get('/v1/browser/summary'),
    ]);

    loading = false;

    if (jobsRes.denied) { denied = true; return; }
    if (jobsRes.unauthorized) { error = 'Not authenticated — check your gateway login.'; return; }
    if (jobsRes.status < 0) { error = jobsRes.error ?? 'Failed to reach gateway'; return; }

    const jobs = jobsRes.data?.jobs ?? [];
    // targets / adapters use real {data:[...]} envelope
    const targetsData = targetsRes.data as Record<string, unknown> | null;
    const targets = (Array.isArray(targetsData?.['data']) ? targetsData!['data'] : []) as unknown[];
    const adaptersData = adaptersRes.data as Record<string, unknown> | null;
    const adapters = (Array.isArray(adaptersData?.['data']) ? adaptersData!['data'] : []) as unknown[];
    // browser summary is a flat object: { total_instances, total_contexts, total_tabs, ... }
    const b = browserRes.data as Record<string, unknown> | null ?? {};

    metrics = {
      jobs_total: jobsRes.data?.total ?? jobs.length,
      jobs_failed: jobs.filter((j) => FAILED_STATES.has((j.status ?? '').toLowerCase())).length,
      targets_total: targets.length,
      adapters_total: adapters.length,
      browser_instances: (b['total_instances'] ?? b['instances'] ?? 0) as number,
      browser_contexts: (b['total_contexts'] ?? b['contexts'] ?? 0) as number,
      browser_tabs: (b['total_tabs'] ?? b['tabs'] ?? 0) as number,
    } as MetricsResponse;

    // Build chart data: pick top 5 numeric keys
    if (metrics) {
      const numeric = Object.entries(metrics)
        .filter(([, v]) => typeof v === 'number' && Number.isFinite(v))
        .sort(([, a], [, b2]) => (b2 as number) - (a as number))
        .slice(0, 5);
      chartLabels = numeric.map(([k]) => k.replace(/_/g, ' '));
      chartValues = numeric.map(([, v]) => v as number);
    }
  }

  // Build/rebuild chart when canvas is bound and data is ready
  $effect(() => {
    if (chartCanvas && chartLabels.length > 0) {
      buildChart();
    }
  });

  onMount(() => {
    load();
    return () => chart?.destroy();
  });

  // All metric entries for card view
  let allEntries = $derived(
    metrics
      ? Object.entries(metrics).filter(([, v]) => v !== null && v !== undefined)
      : []
  );

  function fmtValue(v: unknown): string {
    if (typeof v === 'number') return v.toLocaleString();
    if (typeof v === 'object') return JSON.stringify(v);
    return String(v);
  }
</script>

<div class="space-y-6">
  <div class="flex items-center justify-between">
    <h1 class="text-2xl font-display font-bold text-ink">Metrics</h1>
    <div class="flex items-center gap-3">
      <button
        onclick={() => (grafanaVisible = !grafanaVisible)}
        class="text-sm text-accent-deep hover:underline"
      >
        {grafanaVisible ? 'Hide Grafana' : 'Show Grafana Dashboard'}
      </button>
      <button onclick={() => load()} class="text-sm text-accent-deep hover:underline">Refresh</button>
    </div>
  </div>

  <!-- Grafana iframe (optional) -->
  {#if grafanaVisible}
    <section aria-labelledby="grafana-heading">
      <h2 id="grafana-heading" class="text-base font-semibold text-ink mb-2">
        Grafana
        <span class="text-xs font-normal text-ink-mute font-mono ml-2">{grafanaUrl}</span>
      </h2>
      {#if grafanaError}
        <div class="rounded-md border border-rule bg-paper-soft p-4 text-sm text-ink-mute">
          Grafana is not available at <code class="font-mono">{grafanaUrl}</code>.
          Make sure Grafana is running on port 3000 with the <code class="font-mono">ubag-main</code> dashboard.
        </div>
      {:else}
        <div class="rounded-md border border-rule overflow-hidden" style="height: 480px">
          <iframe
            src={grafanaUrl}
            title="UBAG Grafana Dashboard"
            class="w-full h-full"
            onload={() => (grafanaError = false)}
            onerror={() => (grafanaError = true)}
            sandbox="allow-scripts allow-same-origin allow-forms"
          ></iframe>
        </div>
        <p class="text-xs text-ink-mute mt-1">
          If the iframe is blank, Grafana may not be running or may require login.
        </p>
      {/if}
    </section>
  {/if}

  <!-- Metric cards -->
  {#if loading}
    <div class="text-ink-mute text-sm">Loading metrics…</div>
  {:else if denied}
    <DeniedPanel resource="metrics" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else if !metrics || allEntries.length === 0}
    <EmptyState message="No metrics available." hint="The gateway may not expose /v1/metrics." />
  {:else}
    <!-- Bar chart for top 5 numeric metrics -->
    {#if chartLabels.length > 0}
      <section aria-labelledby="chart-heading">
        <h2 id="chart-heading" class="text-base font-semibold text-ink mb-3">
          Top metrics
        </h2>
        <div class="rounded-md border border-rule bg-paper-soft p-4" style="max-width: 600px">
          <canvas bind:this={chartCanvas} aria-label="Top metrics bar chart"></canvas>
        </div>
      </section>
    {/if}

    <!-- All metric cards -->
    <section aria-labelledby="all-metrics-heading">
      <h2 id="all-metrics-heading" class="text-base font-semibold text-ink mb-3">All Metrics</h2>
      <div class="grid gap-3 grid-cols-2 sm:grid-cols-3 lg:grid-cols-4">
        {#each allEntries as [key, value]}
          <div class="rounded-md border border-rule bg-paper-soft p-3 space-y-1">
            <p class="text-xs text-ink-mute font-mono truncate" title={key}>{key.replace(/_/g, ' ')}</p>
            <p class="text-lg font-display font-bold text-ink truncate" title={fmtValue(value)}>
              {typeof value === 'number' ? value.toLocaleString() : fmtValue(value)}
            </p>
          </div>
        {/each}
      </div>
    </section>
  {/if}
</div>
