<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import type { Webhook } from '$lib/api/types';

  interface Delivery {
    id: string;
    attempt: number;
    status: string;
    timestamp: string;
    response_code?: number;
  }

  interface DeliveriesResponse {
    deliveries: Delivery[];
  }

  // --- Webhooks list state ---
  let webhooks = $state<Webhook[]>([]);
  let loading = $state(true);
  let denied = $state(false);
  let error = $state<string | null>(null);

  // --- Per-webhook deliveries panel state ---
  let expandedId = $state<string | null>(null);
  let deliveriesMap = $state<Record<string, Delivery[]>>({});
  let deliveriesLoading = $state<Record<string, boolean>>({});
  let deliveriesError = $state<Record<string, string | null>>({});

  // --- Per-delivery replay state ---
  let replayState = $state<Record<string, { loading: boolean; success: string | null; error: string | null }>>({});

  async function load() {
    loading = true;
    error = null;
    denied = false;
    const res = await api.get<{ webhooks: Webhook[] }>('/v1/webhooks');
    loading = false;
    if (res.denied) { denied = true; return; }
    if (res.error) { error = res.error; return; }
    webhooks = res.data?.webhooks ?? [];
  }

  async function loadDeliveries(webhookId: string) {
    if (expandedId === webhookId) {
      // Toggle collapse
      expandedId = null;
      return;
    }
    expandedId = webhookId;

    if (deliveriesMap[webhookId]) return; // already loaded

    deliveriesLoading = { ...deliveriesLoading, [webhookId]: true };
    deliveriesError = { ...deliveriesError, [webhookId]: null };

    const res = await api.get<DeliveriesResponse>(`/v1/webhooks/${webhookId}/deliveries`);

    deliveriesLoading = { ...deliveriesLoading, [webhookId]: false };
    if (res.error) {
      deliveriesError = { ...deliveriesError, [webhookId]: res.error };
    } else {
      deliveriesMap = { ...deliveriesMap, [webhookId]: res.data?.deliveries ?? [] };
    }
  }

  async function replay(webhookId: string, deliveryId: string) {
    const key = `${webhookId}:${deliveryId}`;
    replayState = { ...replayState, [key]: { loading: true, success: null, error: null } };

    const res = await api.post(`/v1/webhooks/${webhookId}/deliveries/${deliveryId}/replay`, {});

    if (res.error) {
      replayState = { ...replayState, [key]: { loading: false, success: null, error: res.error } };
    } else {
      replayState = { ...replayState, [key]: { loading: false, success: 'Replayed', error: null } };
      // Refresh deliveries for this webhook
      delete deliveriesMap[webhookId];
      deliveriesMap = { ...deliveriesMap };
      await loadDeliveriesSilent(webhookId);
    }
  }

  async function loadDeliveriesSilent(webhookId: string) {
    deliveriesLoading = { ...deliveriesLoading, [webhookId]: true };
    const res = await api.get<DeliveriesResponse>(`/v1/webhooks/${webhookId}/deliveries`);
    deliveriesLoading = { ...deliveriesLoading, [webhookId]: false };
    if (!res.error) {
      deliveriesMap = { ...deliveriesMap, [webhookId]: res.data?.deliveries ?? [] };
    }
  }

  function fmtDate(s: string): string {
    try { return new Date(s).toLocaleString(); } catch { return s; }
  }

  function truncate(s: string | undefined, n: number): string {
    if (!s) return '—';
    return s.length > n ? s.slice(0, n) + '…' : s;
  }

  function fmtEvents(events: string[] | undefined): string {
    if (!events || events.length === 0) return '*';
    if (events.length <= 3) return events.join(', ');
    return `${events.slice(0, 3).join(', ')} +${events.length - 3}`;
  }

  onMount(() => load());
</script>

<div class="space-y-4">
  <!-- Header -->
  <div class="flex items-center justify-between">
    <div>
      <h1 class="text-2xl font-display font-bold text-ink">Webhooks</h1>
      <p class="text-xs text-ink-mute mt-0.5">Registered endpoints and delivery history</p>
    </div>
    <button onclick={() => load()} class="text-sm text-accent-deep hover:underline">Refresh</button>
  </div>

  {#if loading}
    <div class="text-ink-mute text-sm animate-pulse">Loading…</div>
  {:else if denied}
    <DeniedPanel resource="webhooks" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else if webhooks.length === 0}
    <EmptyState message="No webhooks registered." hint="Webhooks appear here once created via the gateway API." />
  {:else}
    <div class="rounded-md border border-rule overflow-x-auto">
      <table class="w-full text-sm">
        <thead class="bg-paper-soft border-b border-rule">
          <tr>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">ID</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">URL</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Events</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Status</th>
            <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-rule">
          {#each webhooks as wh (wh.id)}
            {@const isExpanded = expandedId === wh.id}
            {@const deliveries = deliveriesMap[wh.id]}
            {@const dlLoading = deliveriesLoading[wh.id]}
            {@const dlError = deliveriesError[wh.id]}

            <!-- Webhook row -->
            <tr class="hover:bg-paper-soft transition-colors">
              <td class="px-4 py-2.5 font-mono text-ink-mute text-xs">{wh.id.slice(0, 8)}…</td>
              <td class="px-4 py-2.5 text-ink-soft text-xs max-w-[18rem] truncate" title={wh.url}>
                {truncate(wh.url, 60)}
              </td>
              <td class="px-4 py-2.5 text-xs text-ink-soft font-mono">{fmtEvents(wh.events)}</td>
              <td class="px-4 py-2.5"><StatusBadge status={wh.status ?? 'active'} /></td>
              <td class="px-4 py-2.5">
                <button
                  onclick={() => loadDeliveries(wh.id)}
                  class="px-3 py-1 rounded-md border border-rule bg-paper-soft text-xs font-medium text-ink hover:bg-paper-warm transition-colors"
                >
                  {isExpanded ? 'Hide Deliveries' : 'View Deliveries'}
                </button>
              </td>
            </tr>

            <!-- Deliveries expandable sub-panel -->
            {#if isExpanded}
              <tr>
                <td colspan="5" class="px-6 py-3 bg-paper-warm border-b border-rule">
                  {#if dlLoading}
                    <p class="text-xs text-ink-mute animate-pulse">Loading deliveries…</p>
                  {:else if dlError}
                    <p class="text-xs text-danger">Error: {dlError}</p>
                  {:else if !deliveries || deliveries.length === 0}
                    <p class="text-xs text-ink-mute italic">No deliveries recorded for this webhook.</p>
                  {:else}
                    <table class="w-full text-xs">
                      <thead>
                        <tr class="text-ink-mute uppercase tracking-wider font-mono">
                          <th class="pb-1.5 text-left pr-6">Attempt #</th>
                          <th class="pb-1.5 text-left pr-6">Status</th>
                          <th class="pb-1.5 text-left pr-6">Response Code</th>
                          <th class="pb-1.5 text-left pr-6">Timestamp</th>
                          <th class="pb-1.5 text-left">Replay</th>
                        </tr>
                      </thead>
                      <tbody class="divide-y divide-rule">
                        {#each deliveries as d (d.id)}
                          {@const rkey = `${wh.id}:${d.id}`}
                          {@const rs = replayState[rkey]}
                          <tr>
                            <td class="py-2 pr-6 font-mono text-ink">{d.attempt}</td>
                            <td class="py-2 pr-6"><StatusBadge status={d.status} /></td>
                            <td class="py-2 pr-6 font-mono text-ink-mute">{d.response_code ?? '—'}</td>
                            <td class="py-2 pr-6 text-ink-mute">{fmtDate(d.timestamp)}</td>
                            <td class="py-2">
                              <div class="flex items-center gap-2">
                                <button
                                  onclick={() => replay(wh.id, d.id)}
                                  disabled={rs?.loading}
                                  class="px-2 py-0.5 rounded border border-rule bg-paper text-xs font-medium text-ink hover:bg-paper-soft disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
                                >
                                  {rs?.loading ? 'Replaying…' : 'Replay'}
                                </button>
                                {#if rs?.success}
                                  <span class="text-success">{rs.success}</span>
                                {/if}
                                {#if rs?.error}
                                  <span class="text-danger">{rs.error}</span>
                                {/if}
                              </div>
                            </td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  {/if}
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>
