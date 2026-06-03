<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';

  // Real gateway shape: { profile, enabled, entries: [] }
  interface CacheEntry {
    key?: string;
    size?: number;
    expires_at?: string;
    [k: string]: unknown;
  }

  interface CacheConfig {
    profile?: string;
    enabled?: boolean;
    entries?: CacheEntry[];
    // Legacy / extra fields passed through
    [key: string]: unknown;
  }

  let cacheConfig = $state<CacheConfig | null>(null);
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
    const res = await api.get('/v1/cache');
    loading = false;
    if (res.denied) { denied = true; return; }
    if (res.error) { error = res.error; return; }
    // /v1/cache returns { profile, enabled, entries: [] } — not a list envelope
    cacheConfig = (res.data as CacheConfig | null) ?? null;
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

  function fmtDate(s?: string): string {
    if (!s) return '—';
    try { return new Date(s).toLocaleString(); } catch { return s; }
  }

  // Extra keys not shown in the primary cards
  const PRIMARY_KEYS = new Set(['profile', 'enabled', 'entries']);

  function extraKeys(cfg: CacheConfig): string[] {
    return Object.keys(cfg).filter(k => !PRIMARY_KEYS.has(k));
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
    <!-- Summary cards -->
    <div class="grid grid-cols-2 gap-4 sm:grid-cols-3">
      <div class="rounded-md border border-rule bg-paper-soft p-4">
        <p class="text-xs font-mono text-ink-mute uppercase tracking-wider mb-1">Status</p>
        {#if cacheConfig?.enabled === true}
          <p class="text-lg font-display font-bold text-success">Enabled</p>
        {:else if cacheConfig?.enabled === false}
          <p class="text-lg font-display font-bold text-danger">Disabled</p>
        {:else}
          <p class="text-lg font-display font-bold text-ink">—</p>
        {/if}
      </div>
      <div class="rounded-md border border-rule bg-paper-soft p-4">
        <p class="text-xs font-mono text-ink-mute uppercase tracking-wider mb-1">Profile</p>
        <p class="text-lg font-display font-bold text-ink font-mono">{cacheConfig?.profile ?? '—'}</p>
      </div>
      <div class="rounded-md border border-rule bg-paper-soft p-4">
        <p class="text-xs font-mono text-ink-mute uppercase tracking-wider mb-1">Entries</p>
        <p class="text-2xl font-display font-bold text-ink">{cacheConfig?.entries?.length ?? 0}</p>
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

    <!-- Entries table -->
    {#if cacheConfig?.entries && cacheConfig.entries.length > 0}
      <div>
        <h2 class="text-base font-semibold text-ink mb-2">Cache Entries</h2>
        <div class="rounded-md border border-rule overflow-x-auto">
          <table class="w-full text-sm">
            <thead class="bg-paper-soft border-b border-rule">
              <tr>
                <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Key</th>
                <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Size</th>
                <th class="px-4 py-2.5 text-left font-medium text-ink-mute text-xs uppercase tracking-wider">Expires At</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-rule">
              {#each cacheConfig.entries as entry, i (entry.key ?? i)}
                <tr class="hover:bg-paper-soft transition-colors">
                  <td class="px-4 py-2.5 font-mono text-xs text-ink">{entry.key ?? '—'}</td>
                  <td class="px-4 py-2.5 text-xs text-ink-soft">{entry.size != null ? `${entry.size} B` : '—'}</td>
                  <td class="px-4 py-2.5 text-xs text-ink-mute">{fmtDate(entry.expires_at)}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </div>
    {:else if cacheConfig}
      <EmptyState message="Cache is empty." hint="No entries are currently cached." />
    {/if}

    <!-- Extra keys -->
    {#if cacheConfig}
      {@const extra = extraKeys(cacheConfig)}
      {#if extra.length > 0}
        <div class="rounded-md border border-rule bg-paper-soft p-4">
          <p class="text-xs font-mono text-ink-mute uppercase tracking-wider mb-2">Additional Fields</p>
          <dl class="space-y-1 text-sm">
            {#each extra as k}
              <div class="flex gap-3">
                <dt class="font-mono text-ink-mute w-40 shrink-0">{k}</dt>
                <dd class="text-ink font-mono text-xs break-all">{JSON.stringify(cacheConfig![k])}</dd>
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
