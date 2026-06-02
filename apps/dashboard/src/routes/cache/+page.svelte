<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';

  interface CacheStats {
    hit_rate?: number;
    total_entries?: number;
    total_size?: number;
    last_purge?: string;
    [key: string]: unknown;
  }

  let stats = $state<CacheStats | null>(null);
  let loading = $state(true);
  let denied = $state(false);
  let error = $state<string | null>(null);

  // Purge state
  let purgeConfirmOpen = $state(false);
  let purgeLoading = $state(false);
  let purgeError = $state<string | null>(null);
  let purgeSuccess = $state<string | null>(null);
  let confirmDialogEl = $state<HTMLDialogElement | null>(null);

  async function load() {
    loading = true;
    error = null;
    denied = false;
    const res = await api.get<CacheStats>('/v1/cache');
    loading = false;
    if (res.denied) { denied = true; return; }
    if (res.error) { error = res.error; return; }
    stats = res.data;
  }

  function openPurgeConfirm() {
    purgeError = null;
    purgeSuccess = null;
    purgeConfirmOpen = true;
    requestAnimationFrame(() => { confirmDialogEl?.showModal(); });
  }

  function closePurgeConfirm() {
    purgeConfirmOpen = false;
    confirmDialogEl?.close();
  }

  async function doPurge() {
    purgeLoading = true;
    purgeError = null;
    purgeSuccess = null;

    // Try POST /v1/cache/purge first, fall back to DELETE /v1/cache
    let res = await api.post<unknown>('/v1/cache/purge');
    if (res.status === 404 || res.status === 405) {
      res = await api.delete<unknown>('/v1/cache');
    }

    purgeLoading = false;
    closePurgeConfirm();

    if (res.error && res.status !== 200 && res.status !== 204) {
      purgeError = `Purge failed: ${res.error} (HTTP ${res.status})`;
    } else {
      purgeSuccess = `Cache purged successfully (HTTP ${res.status}).`;
      await load();
    }
  }

  function fmtSize(bytes?: number): string {
    if (bytes == null) return '—';
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
    return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
  }

  function fmtDate(s?: string): string {
    if (!s) return '—';
    try { return new Date(s).toLocaleString(); } catch { return s; }
  }

  function fmtHitRate(v?: number): string {
    if (v == null) return '—';
    return `${(v * 100).toFixed(1)}%`;
  }

  onMount(() => load());
</script>

<div class="space-y-6">
  <div class="flex items-center justify-between">
    <h1 class="text-2xl font-display font-bold text-ink">Cache</h1>
    <button onclick={() => load()} class="text-sm text-accent-deep hover:underline">Refresh</button>
  </div>

  {#if loading}
    <div class="text-ink-mute text-sm">Loading…</div>
  {:else if denied}
    <DeniedPanel resource="cache statistics" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else}
    <!-- Stats grid -->
    <div class="grid grid-cols-2 gap-4 sm:grid-cols-4">
      <div class="rounded-md border border-rule bg-paper-soft p-4">
        <p class="text-xs font-mono text-ink-mute uppercase tracking-wider mb-1">Hit Rate</p>
        <p class="text-2xl font-display font-bold text-ink">{fmtHitRate(stats?.hit_rate)}</p>
      </div>
      <div class="rounded-md border border-rule bg-paper-soft p-4">
        <p class="text-xs font-mono text-ink-mute uppercase tracking-wider mb-1">Total Entries</p>
        <p class="text-2xl font-display font-bold text-ink">{stats?.total_entries ?? '—'}</p>
      </div>
      <div class="rounded-md border border-rule bg-paper-soft p-4">
        <p class="text-xs font-mono text-ink-mute uppercase tracking-wider mb-1">Total Size</p>
        <p class="text-2xl font-display font-bold text-ink">{fmtSize(stats?.total_size)}</p>
      </div>
      <div class="rounded-md border border-rule bg-paper-soft p-4">
        <p class="text-xs font-mono text-ink-mute uppercase tracking-wider mb-1">Last Purge</p>
        <p class="text-sm font-mono text-ink">{fmtDate(stats?.last_purge)}</p>
      </div>
    </div>

    <!-- Action feedback -->
    {#if purgeError}
      <div class="rounded-md border border-danger/30 bg-danger-soft px-4 py-3 text-sm text-danger" role="alert">{purgeError}</div>
    {/if}
    {#if purgeSuccess}
      <div class="rounded-md border border-success/30 bg-success-soft px-4 py-3 text-sm text-success" role="status">{purgeSuccess}</div>
    {/if}

    <!-- Purge button -->
    <div>
      <button
        onclick={openPurgeConfirm}
        class="px-4 py-2 rounded-md border border-danger/40 bg-danger-soft text-danger text-sm font-medium hover:bg-danger/10 transition-colors"
      >
        Purge Cache
      </button>
    </div>

    <!-- Raw stats (extra keys) -->
    {#if stats}
      {@const extraKeys = Object.keys(stats).filter(k => !['hit_rate','total_entries','total_size','last_purge'].includes(k))}
      {#if extraKeys.length > 0}
        <div class="rounded-md border border-rule bg-paper-soft p-4">
          <p class="text-xs font-mono text-ink-mute uppercase tracking-wider mb-2">Additional Stats</p>
          <dl class="space-y-1 text-sm">
            {#each extraKeys as k}
              <div class="flex gap-3">
                <dt class="font-mono text-ink-mute w-40 shrink-0">{k}</dt>
                <dd class="text-ink font-mono text-xs break-all">{JSON.stringify(stats![k])}</dd>
              </div>
            {/each}
          </dl>
        </div>
      {/if}
    {/if}
  {/if}
</div>

<!-- Purge confirmation dialog -->
{#if purgeConfirmOpen}
  <dialog
    bind:this={confirmDialogEl}
    class="w-full max-w-md rounded-lg border border-rule bg-paper shadow-2xl p-0 backdrop:bg-ink/40"
    aria-label="Confirm cache purge"
    onclose={closePurgeConfirm}
  >
    <div class="px-5 py-4 border-b border-rule bg-paper-soft">
      <h2 class="text-lg font-display font-semibold text-ink">Confirm Purge</h2>
    </div>
    <div class="p-5">
      <p class="text-sm text-ink-soft">
        This will delete all cached entries. This action cannot be undone. Are you sure?
      </p>
    </div>
    <div class="px-5 py-3 border-t border-rule flex justify-end gap-3">
      <button
        onclick={closePurgeConfirm}
        class="px-4 py-2 rounded-md border border-rule bg-paper-soft text-ink text-sm font-medium hover:bg-paper-warm transition-colors"
      >
        Cancel
      </button>
      <button
        onclick={doPurge}
        disabled={purgeLoading}
        class="px-4 py-2 rounded-md border border-danger/40 bg-danger-soft text-danger text-sm font-medium hover:bg-danger/10 disabled:opacity-40 disabled:cursor-not-allowed transition-colors"
      >
        {purgeLoading ? 'Purging…' : 'Purge'}
      </button>
    </div>
  </dialog>
{/if}
