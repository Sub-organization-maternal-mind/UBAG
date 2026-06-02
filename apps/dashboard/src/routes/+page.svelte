<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import type { MetricsResponse, Job } from '$lib/api/types';

  let metrics = $state<MetricsResponse | null>(null);
  let recentJobs = $state<Job[]>([]);

  let metricsLoading = $state(true);
  let jobsLoading = $state(true);
  let metricsDenied = $state(false);
  let metricsError = $state<string | null>(null);
  let jobsDenied = $state(false);
  let jobsError = $state<string | null>(null);

  async function loadMetrics() {
    metricsLoading = true;
    metricsError = null;
    metricsDenied = false;
    const res = await api.get<MetricsResponse>('/v1/metrics');
    metricsLoading = false;
    if (res.denied) { metricsDenied = true; return; }
    if (res.error) { metricsError = res.error; return; }
    metrics = res.data;
  }

  async function loadJobs() {
    jobsLoading = true;
    jobsError = null;
    jobsDenied = false;
    const res = await api.get<{ jobs: Job[] }>('/v1/jobs?limit=5');
    jobsLoading = false;
    if (res.denied) { jobsDenied = true; return; }
    if (res.error) { jobsError = res.error; return; }
    recentJobs = res.data?.jobs ?? [];
  }

  async function load() {
    await Promise.all([loadMetrics(), loadJobs()]);
  }

  onMount(load);

  function fmt(val: unknown): string {
    if (val == null) return '--';
    return String(val);
  }

  function fmtDate(s: string): string {
    try {
      return new Date(s).toLocaleString();
    } catch {
      return s;
    }
  }
</script>

<div class="space-y-6">
  <div class="flex items-center justify-between">
    <h1 class="text-2xl font-display font-bold text-ink">Overview</h1>
    <button onclick={load} class="text-sm text-accent-deep hover:underline">Refresh</button>
  </div>

  <!-- Metric cards -->
  {#if metricsLoading}
    <div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
      {#each [1, 2, 3, 4] as _}
        <div class="rounded-md border border-rule bg-paper-soft p-5 animate-pulse h-24"></div>
      {/each}
    </div>
  {:else if metricsDenied}
    <DeniedPanel resource="metrics" />
  {:else}
    <div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
      <div class="rounded-md border border-rule bg-paper-soft p-5">
        <p class="text-xs font-mono text-ink-mute uppercase tracking-widest mb-1">Total Jobs</p>
        <p class="text-3xl font-display font-bold text-ink">{fmt(metrics?.jobs_total)}</p>
      </div>
      <div class="rounded-md border border-rule bg-paper-soft p-5">
        <p class="text-xs font-mono text-ink-mute uppercase tracking-widest mb-1">Active Sessions</p>
        <p class="text-3xl font-display font-bold text-ink">{fmt(metrics?.browser_instances)}</p>
      </div>
      <div class="rounded-md border border-rule bg-paper-soft p-5">
        <p class="text-xs font-mono text-ink-mute uppercase tracking-widest mb-1">Connected Targets</p>
        <p class="text-3xl font-display font-bold text-ink">{fmt(metrics?.targets_total)}</p>
      </div>
      <div class="rounded-md border border-rule bg-danger-soft border-danger/30 p-5">
        <p class="text-xs font-mono text-danger/70 uppercase tracking-widest mb-1">Failed Jobs</p>
        <p class="text-3xl font-display font-bold text-danger">{fmt(metrics?.jobs_failed)}</p>
      </div>
    </div>
    {#if metricsError}
      <ErrorPanel message={metricsError} retry={loadMetrics} />
    {/if}
  {/if}

  <!-- Recent activity -->
  <div>
    <h2 class="text-lg font-display font-semibold text-ink mb-3">Recent Activity</h2>

    {#if jobsLoading}
      <div class="text-ink-mute text-sm">Loading…</div>
    {:else if jobsDenied}
      <DeniedPanel resource="jobs" />
    {:else if jobsError}
      <ErrorPanel message={jobsError} retry={loadJobs} />
    {:else if recentJobs.length === 0}
      <p class="text-ink-mute text-sm">No recent jobs.</p>
    {:else}
      <div class="rounded-md border border-rule overflow-hidden">
        <table class="w-full text-sm">
          <thead class="bg-paper-soft border-b border-rule">
            <tr>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">ID</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Target</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Type</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Status</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Created</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-rule">
            {#each recentJobs as job}
              <tr class="hover:bg-paper-soft transition-colors">
                <td class="px-4 py-2.5 font-mono text-ink-mute text-xs">{job.id.slice(0, 8)}…</td>
                <td class="px-4 py-2.5 text-ink-soft">{job.target}</td>
                <td class="px-4 py-2.5 text-ink-soft font-mono text-xs">{job.command_type}</td>
                <td class="px-4 py-2.5"><StatusBadge status={job.status} /></td>
                <td class="px-4 py-2.5 text-ink-mute text-xs">{fmtDate(job.created_at)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <div class="mt-2">
        <a href="/jobs" class="text-sm text-accent-deep hover:underline">View all jobs →</a>
      </div>
    {/if}
  </div>
</div>
