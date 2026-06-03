<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';

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
  interface QuotasResponse { quotas?: QuotaEntry[]; usage?: QuotaEntry[]; [key: string]: unknown; }

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
    // Try /v1/quotas first, fall back to /v1/billing
    let res = await api.get<QuotasResponse>('/v1/quotas');
    if ((res.status === 404 || res.error) && !res.denied) {
      res = await api.get<QuotasResponse>('/v1/billing');
    }
    quotasLoading = false;
    if (res.denied) { quotasDenied = true; return; }
    if (res.error && res.status !== 404) { quotasError = res.error; return; }
    const d = res.data;
    quotas = d?.quotas ?? d?.usage ?? [];
    if (!Array.isArray(quotas) && d) {
      // Attempt to normalise object-keyed quotas
      quotas = Object.entries(d)
        .filter(([, v]) => typeof v === 'object' && v !== null && 'limit' in (v as object))
        .map(([name, v]) => {
          const o = v as Record<string, unknown>;
          return {
            name,
            used: Number(o.used ?? o.current ?? 0),
            limit: Number(o.limit ?? o.max ?? 0),
            unit: o.unit != null ? String(o.unit) : undefined,
          };
        });
    }
  }

  onMount(() => {
    loadRateLimits();
    loadQuotas();
  });
</script>

<div class="space-y-8">
  <h1 class="text-2xl font-display font-bold text-ink">Quotas &amp; Billing</h1>

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
      <button onclick={() => loadRateLimits()} class="text-sm text-accent-deep hover:underline">Refresh</button>
    </div>

    {#if rateLimitsLoading}
      <div class="text-ink-mute text-sm">Loading rate limits…</div>
    {:else if rateLimitsDenied}
      <DeniedPanel resource="rate limits" />
    {:else if rateLimitsError}
      <ErrorPanel message={rateLimitsError} retry={loadRateLimits} />
    {:else if policies.length === 0}
      <EmptyState message="No rate limit policies configured." />
    {:else}
      <div class="rounded-md border border-rule overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="bg-paper-soft border-b border-rule">
            <tr>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Action</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Limit</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Window</th>
              <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Burst</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-rule">
            {#each policies as policy, i (policy.action ?? i)}
              <tr class="hover:bg-paper-soft transition-colors">
                <td class="px-4 py-2.5 font-mono text-xs text-ink">{policy.action}</td>
                <td class="px-4 py-2.5 text-xs text-ink-soft">{policy.limit}</td>
                <td class="px-4 py-2.5 text-xs text-ink-soft">{policy.window_seconds}s</td>
                <td class="px-4 py-2.5 text-xs text-ink-mute">{policy.burst ?? '—'}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </section>

  <!-- Quota Usage -->
  <section aria-labelledby="quotas-heading">
    <div class="flex items-center justify-between mb-3">
      <h2 id="quotas-heading" class="text-lg font-display font-semibold text-ink">Quota Usage</h2>
      <button onclick={() => loadQuotas()} class="text-sm text-accent-deep hover:underline">Refresh</button>
    </div>

    {#if quotasLoading}
      <div class="text-ink-mute text-sm">Loading quotas…</div>
    {:else if quotasDenied}
      <DeniedPanel resource="quotas and billing" />
    {:else if quotasError}
      <ErrorPanel message={quotasError} retry={loadQuotas} />
    {:else if quotas.length === 0}
      <EmptyState message="No quota data available." hint="The gateway may not expose /v1/quotas or /v1/billing." />
    {:else}
      <div class="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {#each quotas as q, i (q.name ?? i)}
          {@const p = pct(q.used, q.limit)}
          <div class="rounded-md border border-rule bg-paper-soft p-4 space-y-2">
            <div class="flex items-baseline justify-between gap-2">
              <p class="text-sm font-medium text-ink truncate">{q.name}</p>
              <p class="text-xs text-ink-mute shrink-0">{p}%</p>
            </div>
            <div class="h-2 rounded-full bg-rule overflow-hidden">
              <div
                class="h-full rounded-full transition-all {barColor(p)}"
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
