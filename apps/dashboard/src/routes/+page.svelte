<script lang="ts">
  import { onMount } from 'svelte';
  import { base } from '$app/paths';
  import { api } from '$lib/api/client';
  import { failedCount, normalizeJobs, parseJobsSummary } from '$lib/api/jobs';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import SkeletonCards from '$lib/components/SkeletonCards.svelte';
  import SkeletonTable from '$lib/components/SkeletonTable.svelte';
  import UpdatedAgo from '$lib/components/UpdatedAgo.svelte';
  import type { MetricsResponse, Job } from '$lib/api/types';
  import { FAILED_STATES as FAILED_STATUS_LIST } from '$lib/api/statuses';

  let metrics = $state<MetricsResponse | null>(null);
  let recentJobs = $state<Job[]>([]);

  let metricsLoading = $state(true);
  let jobsLoading = $state(true);
  let metricsDenied = $state(false);
  let metricsError = $state<string | null>(null);
  let jobsDenied = $state(false);
  let jobsError = $state<string | null>(null);
  let lastUpdated = $state<Date | null>(null);

  // Terminal-failure states used to count failed jobs (contract vocabulary).
  const FAILED_STATES = new Set(FAILED_STATUS_LIST);

  type JobsResponse = Awaited<ReturnType<typeof api.get<{ jobs?: Job[]; total?: number }>>>;

  function applyJobsResult(res: JobsResponse) {
    jobsLoading = false;
    if (res.denied) { jobsDenied = true; return; }
    if (res.unauthorized) { jobsError = 'Not authenticated — check your gateway login.'; return; }
    if (res.error) { jobsError = res.error; return; }
    recentJobs = normalizeJobs(res.data?.jobs).slice(0, 5);
  }

  async function loadMetrics(jobsResponse?: JobsResponse) {
    // Derive overview metric cards from real JSON endpoints. The Prometheus
    // /v1/metrics endpoint is intentionally blocked at the edge and is not JSON,
    // so we aggregate counts from the resource endpoints instead.
    metricsLoading = true;
    metricsError = null;
    metricsDenied = false;

    const [jobsRes, targetsRes, browserRes, summaryRes] = await Promise.all([
      jobsResponse ?? api.get<{ jobs?: Job[]; total?: number }>('/v1/jobs?limit=100'),
      api.get('/v1/targets'),
      api.get('/v1/browser/summary'),
      api.get('/v1/jobs/summary'),
    ]);

    metricsLoading = false;

    // If the primary jobs call is denied/unauthorized, surface that state.
    if (jobsRes.denied) { metricsDenied = true; return; }
    if (jobsRes.unauthorized) { metricsError = 'Not authenticated — check your gateway login.'; return; }

    const jobs = jobsRes.data?.jobs ?? [];
    const summary = parseJobsSummary(summaryRes.data);
    // targets uses real {data:[...]} envelope
    const targetsData = targetsRes.data as Record<string, unknown> | null;
    const targets = (Array.isArray(targetsData?.['data']) ? targetsData!['data'] : []) as unknown[];
    // browser summary is a flat object: { total_instances, total_contexts, total_tabs, ... }
    const browserSummary = browserRes.data as Record<string, unknown> | null;
    const browserInstances = (browserSummary?.['total_instances'] ?? browserSummary?.['instances'] ?? 0) as number;

    metrics = {
      // /v1/jobs/summary carries true counts; the list page is capped at 100 rows,
      // so it is only a fallback for gateways that predate the summary route.
      jobs_total: summary?.total ?? jobsRes.data?.total ?? jobs.length,
      jobs_failed: summary
        ? failedCount(summary)
        : jobs.filter((j) => FAILED_STATES.has((j.status ?? '').toLowerCase())).length,
      jobs_queued: summary?.counts_by_status['queued'],
      targets_total: targets.length,
      browser_instances: browserInstances,
    };
  }

  async function loadJobs() {
    jobsLoading = true;
    jobsError = null;
    jobsDenied = false;
    const res = await api.get<{ jobs?: Job[]; total?: number }>('/v1/jobs?limit=100');
    applyJobsResult(res);
  }

  async function load() {
    metricsLoading = true;
    jobsLoading = true;
    metricsError = null;
    jobsError = null;
    metricsDenied = false;
    jobsDenied = false;

    // Both sections consume the same collection. Reusing this response prevents
    // Recent Activity from hanging behind a duplicate concurrent request.
    const jobsRes = await api.get<{ jobs?: Job[]; total?: number }>('/v1/jobs?limit=100');
    applyJobsResult(jobsRes);
    await loadMetrics(jobsRes);
    lastUpdated = new Date();
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
  <PageHeader title="Overview" subtitle="Gateway pulse at a glance — queue depth, provider pool and live browser capacity.">
    {#snippet actions()}
      <UpdatedAgo at={lastUpdated} />
      <button onclick={load} class="btn btn-secondary btn-sm">Refresh</button>
    {/snippet}
  </PageHeader>

  <!-- Metric cards -->
  {#if metricsLoading}
    <SkeletonCards count={4} cols="grid-cols-1 sm:grid-cols-2 xl:grid-cols-4" />
  {:else if metricsDenied}
    <DeniedPanel resource="metrics" />
  {:else}
    <div class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4">
      <div class="card">
        <p class="mb-1 text-xs font-mono uppercase tracking-widest text-ink-mute">Total Jobs</p>
        <p class="text-3xl font-display font-bold text-ink">{fmt(metrics?.jobs_total)}</p>
      </div>
      <div class="card">
        <p class="mb-1 text-xs font-mono uppercase tracking-widest text-ink-mute">Active Sessions</p>
        <p class="text-3xl font-display font-bold text-ink">{fmt(metrics?.browser_instances)}</p>
      </div>
      <div class="card">
        <p class="mb-1 text-xs font-mono uppercase tracking-widest text-ink-mute">Connected Targets</p>
        <p class="text-3xl font-display font-bold text-ink">{fmt(metrics?.targets_total)}</p>
      </div>
      <div class="card border-danger/30 bg-danger-soft">
        <p class="mb-1 text-xs font-mono uppercase tracking-widest text-danger">Failed Jobs</p>
        <p class="text-3xl font-display font-bold text-danger">{fmt(metrics?.jobs_failed)}</p>
      </div>
    </div>
    {#if metricsError}
      <ErrorPanel message={metricsError} retry={loadMetrics} />
    {/if}
  {/if}

  <!-- Recent activity -->
  <section aria-labelledby="recent-activity-heading">
    <h2 id="recent-activity-heading" class="mb-3 text-lg font-display font-semibold text-ink">Recent Activity</h2>

    {#if jobsLoading}
      <SkeletonTable rows={5} cols={5} />
    {:else if jobsDenied}
      <DeniedPanel resource="jobs" />
    {:else if jobsError}
      <ErrorPanel message={jobsError} retry={loadJobs} />
    {:else if recentJobs.length === 0}
      <p class="text-sm text-ink-mute">No recent jobs.</p>
    {:else}
      <div class="table-wrap">
        <table class="w-full text-sm">
          <thead class="thead">
            <tr>
              <th class="th">ID</th>
              <th class="th">Target</th>
              <th class="th">Type</th>
              <th class="th">Status</th>
              <th class="th">Created</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-rule">
            {#each recentJobs as job (job.id)}
              <tr class="transition-colors hover:bg-paper-soft/70">
                <td class="td font-mono text-xs text-ink-mute">{job.id.slice(0, 8)}…</td>
                <td class="td max-w-[12rem] truncate text-ink" title={job.target}>{job.target}</td>
                <td class="td font-mono text-xs">{job.command_type}</td>
                <td class="td"><StatusBadge status={job.status} /></td>
                <td class="td whitespace-nowrap text-xs text-ink-mute">{fmtDate(job.created_at)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <div class="mt-2">
        <a href="{base}/jobs" class="text-sm font-medium text-accent-deep hover:underline">View all jobs →</a>
      </div>
    {/if}
  </section>
</div>
