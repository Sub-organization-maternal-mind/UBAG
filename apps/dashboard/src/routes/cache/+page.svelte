<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '$lib/api/client';
  import ErrorPanel from '$lib/components/ErrorPanel.svelte';
  import DeniedPanel from '$lib/components/DeniedPanel.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import SkeletonCards from '$lib/components/SkeletonCards.svelte';
  import Modal from '$lib/components/Modal.svelte';

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
  let purgeTag = $state('');

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
  }

  function closePurgeConfirm() {
    purgeConfirmOpen = false;
  }

  async function doPurge() {
    purgeLoading = true;
    purgeError = null;
    purgeSuccess = null;

    // Tag-scoped purge uses the served invalidate endpoint; an empty tag
    // purges the whole cache via DELETE /v1/cache.
    const tag = purgeTag.trim();
    const res = tag
      ? await api.post<{ removed?: number }>('/v1/cache/invalidate', { tag })
      : await api.delete<unknown>('/v1/cache');

    purgeLoading = false;
    closePurgeConfirm();

    if (res.error) {
      purgeError = `Purge failed: ${res.error} (HTTP ${res.status})`;
    } else {
      const removed = (res.data as { removed?: number } | null)?.removed;
      purgeSuccess = tag
        ? `Purged tag “${tag}”${typeof removed === 'number' ? ` (${removed} entries)` : ''}.`
        : `Cache purged successfully (HTTP ${res.status}).`;
      purgeTag = '';
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
  <PageHeader title="Cache" subtitle="Gateway response-cache profile, entry count and purge controls.">
    {#snippet actions()}
      <button onclick={() => load()} class="btn btn-secondary btn-sm">Refresh</button>
    {/snippet}
  </PageHeader>

  {#if loading}
    <SkeletonCards count={3} cols="grid-cols-1 sm:grid-cols-3" />
  {:else if denied}
    <DeniedPanel resource="cache statistics" />
  {:else if error}
    <ErrorPanel message={error} retry={load} />
  {:else}
    <!-- Summary cards -->
    <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
      <div class="card">
        <p class="mb-1 text-xs font-mono uppercase tracking-wider text-ink-mute">Status</p>
        {#if cacheConfig?.enabled === true}
          <p class="text-lg font-display font-bold text-success">Enabled</p>
        {:else if cacheConfig?.enabled === false}
          <p class="text-lg font-display font-bold text-danger">Disabled</p>
        {:else}
          <p class="text-lg font-display font-bold text-ink">—</p>
        {/if}
      </div>
      <div class="card">
        <p class="mb-1 text-xs font-mono uppercase tracking-wider text-ink-mute">Profile</p>
        <p class="text-lg font-display font-bold font-mono text-ink">{cacheConfig?.profile ?? '—'}</p>
      </div>
      <div class="card">
        <p class="mb-1 text-xs font-mono uppercase tracking-wider text-ink-mute">Entries</p>
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
      <button onclick={openPurgeConfirm} class="btn btn-danger">Purge Cache</button>
    </div>

    <!-- Entries table -->
    {#if cacheConfig?.entries && cacheConfig.entries.length > 0}
      <div>
        <h2 class="text-base font-semibold text-ink mb-2">Cache Entries</h2>
        <div class="table-wrap">
          <table class="w-full text-sm">
            <thead class="thead">
              <tr>
                <th class="th">Key</th>
                <th class="th">Size</th>
                <th class="th">Expires At</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-rule">
              {#each cacheConfig.entries as entry, i (entry.key ?? i)}
                <tr class="transition-colors hover:bg-paper-soft/70">
                  <td class="td font-mono text-xs text-ink break-all">{entry.key ?? '—'}</td>
                  <td class="td text-xs">{entry.size != null ? `${entry.size} B` : '—'}</td>
                  <td class="td text-xs text-ink-mute whitespace-nowrap">{fmtDate(entry.expires_at)}</td>
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
        <div class="card">
          <p class="mb-2 text-xs font-mono uppercase tracking-wider text-ink-mute">Additional Fields</p>
          <dl class="space-y-1 text-sm">
            {#each extra as k (k)}
              <div class="flex gap-3">
                <dt class="w-40 shrink-0 font-mono text-ink-mute">{k}</dt>
                <dd class="break-all font-mono text-xs text-ink">{JSON.stringify(cacheConfig![k])}</dd>
              </div>
            {/each}
          </dl>
        </div>
      {/if}
    {/if}
  {/if}
</div>

<!-- Purge confirmation dialog -->
<Modal bind:open={purgeConfirmOpen} title="Confirm Purge" width="sm">
  <div class="space-y-3">
    <p class="text-sm text-ink-soft">
      Optionally purge only one tag. Leave empty to delete all cached entries. This action cannot be undone. Are you sure?
    </p>
    <label class="block">
      <span class="block text-xs uppercase tracking-wider font-mono text-ink-mute mb-1.5">Tag (optional)</span>
      <input type="text" bind:value={purgeTag} placeholder="e.g. template:radiology_ct_brain_v3" class="input" />
    </label>
  </div>
  {#snippet footer()}
    <button onclick={closePurgeConfirm} class="btn btn-secondary">Cancel</button>
    <button onclick={doPurge} disabled={purgeLoading} class="btn btn-danger">
      {purgeLoading ? 'Purging…' : 'Purge'}
    </button>
  {/snippet}
</Modal>
