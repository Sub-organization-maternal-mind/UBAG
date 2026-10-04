<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import SkeletonCards from '$lib/components/SkeletonCards.svelte';
  import SkeletonTable from '$lib/components/SkeletonTable.svelte';

  // Real gateway shape: { enabled, policies: [ { action, limit, window_seconds, burst? } ] }
  interface RateLimitPolicy {
    action: string;
    limit: number;
    window_seconds: number;
    burst?: number;
  }
  interface RateLimitsResponse {
    enabled?: boolean;
    policies?: RateLimitPolicy[];
    // Legacy fallback keys
    rate_limits?: unknown[];
    limits?: unknown[];
    [key: string]: unknown;
  }

  interface QuotaEntry {
    name: string;
    used: number;
    limit: number;
    unit?: string;
  }
  interface ConcurrencyResponse {
    data?: { target: string; identity_ref: string; in_flight: number; current_cap: number }[];
  }

  let policies = $state<RateLimitPolicy[]>([]);
  let rateLimitsEnabled = $state<boolean | null>(null);
  let rateLimitsLoading = $state(true);
  let rateLimitsDenied = $state(false);
  let rateLimitsError = $state<string | null>(null);

  let quotas = $state<QuotaEntry[]>([]);
  let quotasLoading = $state(true);
  let quotasDenied = $state(false);
  let quotasError = $state<string | null>(null);

  function pct(used: number, limit: number): number {
    if (limit <= 0) return 0;
    return Math.min(100, Math.round((used / limit) * 100));
  }

  function barColor(p: number): string {
    if (p >= 90) return 'bg-danger';
    if (p >= 70) return 'bg-warning';
    return 'bg-success';
  }

  async function loadRateLimits() {
    rateLimitsLoading = true;
    rateLimitsError = null;
    rateLimitsDenied = false;
    const res = await api.get<RateLimitsResponse>('/v1/rate-limits');
    rateLimitsLoading = false;
    if (res.denied) { rateLimitsDenied = true; return; }
    if (res.error && res.status !== 404) { rateLimitsError = res.error; return; }
    const d = res.data;
    rateLimitsEnabled = d?.enabled ?? null;
    // Real shape has policies[]; fall back to legacy rate_limits/limits arrays
    policies = (d?.policies ?? d?.rate_limits ?? d?.limits ?? []) as RateLimitPolicy[];
    if (!Array.isArray(policies)) policies = [];
  }

  async function loadQuotas() {
    quotasLoading = true;
    quotasError = null;
    quotasDenied = false;
    const res = await api.get<ConcurrencyResponse>('/v1/concurrency');
    quotasLoading = false;
    if (res.denied) { quotasDenied = true; return; }
    if (res.error) { quotasError = res.error; return; }
    quotas = (res.data?.data ?? []).map((entry) => ({
      name: entry.identity_ref ? `${entry.target} (${entry.identity_ref})` : entry.target,
      used: entry.in_flight,
      limit: entry.current_cap,
      unit: 'in-flight jobs',
    }));
  }

  onMount(() => {
    loadRateLimits();
    loadQuotas();
  });
</script>

<div class="space-y-8">
  <PageHeader title="Quotas & Limits" subtitle="Rate-limit policies and current concurrency usage reported by the gateway." />

  <!-- Rate Limits -->
  <section aria-labelledby="rate-limits-heading">
    <div class="flex items-center justify-between mb-3">
      <div class="flex items-center gap-3">
        <h2 id="rate-limits-heading" class="text-lg font-display font-semibold text-ink">Rate Limits</h2>
        {#if rateLimitsEnabled === true}
          <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-success-soft text-success">Enabled</span>
        {:else if rateLimitsEnabled === false}
          <span class="inline-flex items-center px-2 py-0.5 rounded text-xs font-medium bg-paper-soft text-ink-mute">Disabled</span>
        {/if}
      </div>
      <button onclick={() => loadRateLimits()} class="btn btn-secondary btn-sm">Refresh</button>
    </div>

    {#if rateLimitsLoading}
      <SkeletonTable rows={4} cols={4} />
    {:else if rateLimitsDenied}
      <DeniedPanel resource="rate limits" />
    {:else if rateLimitsError}
      <ErrorPanel message={rateLimitsError} retry={loadRateLimits} />
    {:else if policies.length === 0}
      <EmptyState message="No rate limit policies configured." />
    {:else}
      <div class="table-wrap">
        <table class="w-full text-sm">
          <thead class="thead">
            <tr>
              <th class="th">Action</th>
              <th class="th">Limit</th>
              <th class="th">Window</th>
              <th class="th">Burst</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-rule">
            {#each policies as policy, i (policy.action ?? i)}
              <tr class="transition-colors hover:bg-paper-soft/70">
                <td class="td font-mono text-xs text-ink">{policy.action}</td>
                <td class="td text-xs">{policy.limit}</td>
                <td class="td text-xs">{policy.window_seconds}s</td>
                <td class="td text-xs text-ink-mute">{policy.burst ?? '—'}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <!-- Concurrency Usage -->
  <section aria-labelledby="quotas-heading">
    <div class="flex items-center justify-between mb-3">
      <h2 id="quotas-heading" class="text-lg font-display font-semibold text-ink">Concurrency Usage</h2>
      <button onclick={() => loadQuotas()} class="btn btn-secondary btn-sm">Refresh</button>
    </div>

    {#if quotasLoading}
      <SkeletonCards count={3} cols="grid-cols-1 sm:grid-cols-2 lg:grid-cols-3" />
    {:else if quotasDenied}
      <DeniedPanel resource="concurrency limits" />
    {:else if quotasError}
      <ErrorPanel message={quotasError} retry={loadQuotas} />
    {:else if quotas.length === 0}
      <EmptyState message="No concurrency data available." hint="No concurrency ceilings have been reported yet." />
    {:else}
      <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {#each quotas as q, i (q.name ?? i)}
          {@const p = pct(q.used, q.limit)}
          <div class="card space-y-2">
            <div class="flex items-baseline justify-between gap-2">
              <p class="text-sm font-medium text-ink truncate" title={q.name}>{q.name}</p>
              <p class="text-xs text-ink-mute shrink-0">{p}%</p>
            </div>
            <div class="h-2 rounded-full bg-rule overflow-hidden">
              <div
                class="h-full rounded-full transition-all duration-500 ease-out {barColor(p)}"
                style="width: {p}%"
                role="progressbar"
                aria-label="{q.name} usage"
                aria-valuenow={q.used}
                aria-valuemin={0}
                aria-valuemax={q.limit}
              ></div>
            </div>
            <p class="text-xs text-ink-mute">
              {q.used.toLocaleString()} / {q.limit.toLocaleString()}{q.unit ? ' ' + q.unit : ''}
            </p>
          </div>
        {/each}
      </div>
    {/if}
  </section>
</div>
