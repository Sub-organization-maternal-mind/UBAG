<script lang="ts">
  import { onMount } from 'svelte';
  import { api, listOf } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import SkeletonTable from '$lib/components/SkeletonTable.svelte';
  import UpdatedAgo from '$lib/components/UpdatedAgo.svelte';
  import Modal from '$lib/components/Modal.svelte';
  import { pollWhileVisible } from '$lib/poll';
  import type { Webhook } from '$lib/api/types';

  interface Delivery {
    id: string;
    attempt: number;
    status: string;
    timestamp: string;
    response_code?: number;
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

  // Replay reason dialog (replaces window.prompt): reason is audit-logged.
  let replayDialog = $state<{ webhookId: string; deliveryId: string } | null>(null);
  let replayReason = $state('');

  function openReplayDialog(webhookId: string, deliveryId: string) {
    replayDialog = { webhookId, deliveryId };
    replayReason = 'operator replay from dashboard';
  }

  function closeReplayDialog() {
    replayDialog = null;
  }

  async function doReplay() {
    if (!replayDialog) return;
    const { webhookId, deliveryId } = replayDialog;
    const key = `${webhookId}:${deliveryId}`;
    const reason = replayReason.trim();
    if (!reason) {
      replayState = { ...replayState, [key]: { loading: false, success: null, error: 'A reason is required for the audit record.' } };
      return;
    }
    replayState = { ...replayState, [key]: { loading: true, success: null, error: null } };

    // Contract: POST /v1/webhooks/replay with {delivery_id, reason} (openapi replayWebhookDelivery).
    const res = await api.post<{ status?: string; delivery_id?: string }>('/v1/webhooks/replay', {
      delivery_id: deliveryId,
      webhook_id: webhookId,
      reason: reason,
    });

    if (res.error) {
      replayState = { ...replayState, [key]: { loading: false, success: null, error: `${res.error} (HTTP ${res.status})` } };
    } else {
      replayState = { ...replayState, [key]: { loading: false, success: 'Replay accepted', error: null } };
      // Refresh deliveries for this webhook
      delete deliveriesMap[webhookId];
      deliveriesMap = { ...deliveriesMap };
      await loadDeliveriesSilent(webhookId);
    }
    closeReplayDialog();
  }

  // --- Secret rotation state (POST /v1/webhooks/secret:rotate) ---
  let rotateOpenId = $state<string | null>(null);
  let rotateRef = $state('');
  let rotateOverlap = $state('3600');
  let rotateLoading = $state(false);
  let rotateError = $state<string | null>(null);
  let rotateSuccess = $state<string | null>(null);

  function openRotate(webhookId: string) {
    rotateOpenId = webhookId;
    rotateRef = '';
    rotateOverlap = '3600';
    rotateError = null;
    rotateSuccess = null;
  }

  function closeRotate() {
    rotateOpenId = null;
  }

  async function doRotate(webhookId: string) {
    const newSecretRef = rotateRef.trim();
    if (!newSecretRef) {
      rotateError = 'A new secret reference is required (secrets are supplied by reference only).';
      return;
    }
    rotateLoading = true;
    rotateError = null;
    rotateSuccess = null;
    const overlap = Number.parseInt(rotateOverlap, 10);
    const res = await api.post<{
      status?: string; active_secret_ref?: string; previous_secret_ref?: string;
    }>('/v1/webhooks/secret:rotate', {
      webhook_id: webhookId,
      new_secret_ref: newSecretRef,
      ...(Number.isFinite(overlap) && overlap > 0 ? { overlap_seconds: overlap } : {}),
    });
    rotateLoading = false;
    if (res.error) {
      rotateError = `Rotation failed: ${res.error} (HTTP ${res.status})`;
      return;
    }
    const active = (res.data as { active_secret_ref?: string } | null)?.active_secret_ref;
    rotateSuccess = `Secret rotated${active ? ` — active ref ${active}` : ''}. Update the receiver before the overlap window ends.`;
  }

  let lastUpdated = $state<Date | null>(null);

  async function load(silent = false) {
    if (!silent) {
      loading = true;
      error = null;
      denied = false;
    }
    const res = await api.get('/v1/webhooks');
    if (!silent) loading = false;
    if (res.denied) { denied = true; return res; }
    if (res.error) { error = res.error; return res; }
    webhooks = listOf<Webhook>(res);
    lastUpdated = new Date();
    return res;
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

    const res = await api.get(`/v1/webhooks/${webhookId}/deliveries`);

    deliveriesLoading = { ...deliveriesLoading, [webhookId]: false };
    if (res.error) {
      deliveriesError = { ...deliveriesError, [webhookId]: res.error };
    } else {
      deliveriesMap = { ...deliveriesMap, [webhookId]: listOf<Delivery>(res, 'deliveries') };
    }
  }

  async function replay(webhookId: string, deliveryId: string) {
    openReplayDialog(webhookId, deliveryId);
  }

  async function loadDeliveriesSilent(webhookId: string) {
    deliveriesLoading = { ...deliveriesLoading, [webhookId]: true };
    const res = await api.get(`/v1/webhooks/${webhookId}/deliveries`);
    deliveriesLoading = { ...deliveriesLoading, [webhookId]: false };
    if (!res.error) {
      deliveriesMap = { ...deliveriesMap, [webhookId]: listOf<Delivery>(res, 'deliveries') };
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

  onMount(() => {
    load();
    const stopPolling = pollWhileVisible(() => load(true), 45_000, { immediate: false });
    return stopPolling;
  });
</script>

<div class="space-y-4">
  <PageHeader title="Webhooks" subtitle="Registered endpoints and delivery history.">
    {#snippet actions()}
      <UpdatedAgo at={lastUpdated} />
      <button onclick={() => load()} class="btn btn-secondary btn-sm">Refresh</button>
    {/snippet}
  </PageHeader>

  {#if loading}
    <SkeletonTable rows={5} cols={5} />
  {:else if denied}
    <DeniedPanel resource="webhooks" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else if webhooks.length === 0}
    <EmptyState message="No webhooks registered." hint="Webhooks appear here once created via the gateway API." />
  {:else}
    <div class="table-wrap">
      <table class="w-full text-sm">
        <thead class="thead">
          <tr>
            <th class="th">ID</th>
            <th class="th">URL</th>
            <th class="th">Events</th>
            <th class="th">Status</th>
            <th class="th">Actions</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-rule">
          {#each webhooks as wh (wh.id)}
            {@const isExpanded = expandedId === wh.id}
            {@const deliveries = deliveriesMap[wh.id]}
            {@const dlLoading = deliveriesLoading[wh.id]}
            {@const dlError = deliveriesError[wh.id]}

            <!-- Webhook row -->
            <tr class="transition-colors hover:bg-paper-soft/70">
              <td class="td font-mono text-xs text-ink-mute">{wh.id.slice(0, 8)}…</td>
              <td class="td text-xs max-w-[18rem] truncate" title={wh.url}>
                {truncate(wh.url, 60)}
              </td>
              <td class="td text-xs font-mono">{fmtEvents(wh.events)}</td>
              <td class="td"><StatusBadge status={wh.status ?? 'active'} /></td>
              <td class="td">
                <div class="flex items-center gap-2">
                  <button onclick={() => loadDeliveries(wh.id)} class="btn btn-secondary btn-sm">
                    {isExpanded ? 'Hide Deliveries' : 'View Deliveries'}
                  </button>
                  <button onclick={() => openRotate(wh.id)} class="btn btn-secondary btn-sm">
                    Rotate Secret
                  </button>
                </div>
              </td>
            </tr>

            <!-- Deliveries expandable sub-panel -->
            {#if isExpanded}
              <tr>
                <td colspan="5" class="bg-paper-warm px-4 sm:px-6 py-3 border-b border-rule">
                  <div class="overflow-x-auto">
                    {#if dlLoading}
                      <p class="text-xs text-ink-mute animate-pulse">Loading deliveries…</p>
                    {:else if dlError}
                      <p class="text-xs text-danger">Error: {dlError}</p>
                    {:else if !deliveries || deliveries.length === 0}
                      <p class="text-xs text-ink-mute italic">No deliveries recorded for this webhook.</p>
                    {:else}
                      <table class="w-full text-xs min-w-[34rem]">
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
                              <td class="py-2 pr-6 text-ink-mute whitespace-nowrap">{fmtDate(d.timestamp)}</td>
                              <td class="py-2">
                                <div class="flex items-center gap-2 flex-wrap">
                                  <button
                                    onclick={() => replay(wh.id, d.id)}
                                    disabled={rs?.loading}
                                    class="btn btn-secondary btn-sm"
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
                  </div>
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<!-- Replay reason dialog -->
<Modal open={replayDialog !== null} title="Replay Delivery" width="sm" onClose={closeReplayDialog}>
  <div class="space-y-3">
    <p class="text-sm text-ink-soft">
      Replaying delivery <code class="font-mono text-xs">{replayDialog?.deliveryId.slice(0, 12)}…</code>.
      The reason is recorded in the audit log.
    </p>
    <label class="block">
      <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Reason *</span>
      <input type="text" bind:value={replayReason} class="input" placeholder="e.g. receiver was down during first attempt" />
    </label>
  </div>
  {#snippet footer()}
    <button onclick={closeReplayDialog} class="btn btn-secondary">Cancel</button>
    <button onclick={doReplay} class="btn btn-primary">Replay</button>
  {/snippet}
</Modal>

<!-- Secret rotation dialog -->
<Modal
  open={rotateOpenId !== null}
  title="Rotate Webhook Secret"
  width="sm"
  onClose={closeRotate}
>
  <div class="space-y-3">
    {#if rotateOpenId}
      <p class="font-mono text-xs text-ink-mute break-all">{rotateOpenId}</p>
    {/if}
    <p class="text-sm text-ink-soft">
      Secrets are supplied by reference only — the plaintext never leaves your secret store.
      The previous secret keeps verifying during the overlap window.
    </p>
    <label class="block">
      <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">New secret reference</span>
      <input type="text" bind:value={rotateRef} placeholder="e.g. vault:ubag/webhooks/acme#signing-key" class="input" />
    </label>
    <label class="block">
      <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Overlap seconds</span>
      <input type="number" min="0" bind:value={rotateOverlap} placeholder="3600" class="input" />
    </label>
    {#if rotateError}
      <div class="rounded-md border border-danger/30 bg-danger-soft px-4 py-3 text-sm text-danger" role="alert">{rotateError}</div>
    {/if}
    {#if rotateSuccess}
      <div class="rounded-md border border-success/30 bg-success-soft px-4 py-3 text-sm text-success" role="status">{rotateSuccess}</div>
    {/if}
  </div>
  {#snippet footer()}
    <button onclick={closeRotate} class="btn btn-secondary">{rotateSuccess ? 'Done' : 'Cancel'}</button>
    {#if !rotateSuccess && rotateOpenId}
      <button onclick={() => { if (rotateOpenId) doRotate(rotateOpenId); }} disabled={rotateLoading} class="btn btn-danger">
        {rotateLoading ? 'Rotating…' : 'Rotate'}
      </button>
    {/if}
  {/snippet}
</Modal>
